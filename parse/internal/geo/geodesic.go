package geo

// Distance ports the distance of geod_inverse from GeographicLib's C
// library (geodesic.c, 2.x), the routine PostGIS measures geography with
// through PROJ:
//
//	C. F. F. Karney, Algorithms for geodesics, J. Geodesy 87, 43–55 (2013),
//	https://doi.org/10.1007/s00190-012-0578-z
//
// The port keeps GeographicLib's license:
//
//	The MIT License (MIT).
//
//	Copyright (c) 2012-2025, Charles Karney
//
//	Permission is hereby granted, free of charge, to any person
//	obtaining a copy of this software and associated documentation
//	files (the "Software"), to deal in the Software without
//	restriction, including without limitation the rights to use, copy,
//	modify, merge, publish, distribute, sublicense, and/or sell copies
//	of the Software, and to permit persons to whom the Software is
//	furnished to do so, subject to the following conditions:
//
//	The above copyright notice and this permission notice shall be
//	included in all copies or substantial portions of the Software.
//
//	THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND,
//	EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF
//	MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND
//	NONINFRINGEMENT.  IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT
//	HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY,
//	WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
//	OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER
//	DEALINGS IN THE SOFTWARE.
//
// The port keeps the WGS 84 ellipsoid only (f > 0) and computes the
// distance alone: no azimuths, scales or area. It follows the C statement
// for statement, so identifiers keep their names there.

import "math"

// WGS 84, as SRID 4326 declares it.
const (
	wgsA = 6378137.0
	wgsF = 1 / 298.257223563

	f1   = 1 - wgsF
	e2   = wgsF * (2 - wgsF)
	ep2  = e2 / (f1 * f1)
	n3   = wgsF / (2 - wgsF) // the third flattening, n in the C source
	wgsB = wgsA * f1
)

const (
	order  = 6 // GEOGRAPHICLIB_GEODESIC_ORDER
	nC     = order + 1
	nC3    = order
	digits = 53

	maxit1 = 20
	maxit2 = maxit1 + digits + 10

	degree = math.Pi / 180
)

var (
	tiny    = math.Sqrt(0x1p-1022) // sqrt(DBL_MIN)
	tol0    = 0x1p-52              // DBL_EPSILON
	tol1    = 200 * tol0
	tol2    = math.Sqrt(tol0)
	tolb    = tol0
	xthresh = 1000 * tol2
	etol2   = 0.1 * tol2 / math.Sqrt(math.Max(0.001, math.Abs(wgsF))*math.Min(1.0, 1-wgsF/2)/2)

	a3x = a3coeff()
	c3x = c3coeff()
)

// Distance is the length in meters of the shortest geodesic between a and
// b on the WGS 84 ellipsoid, accurate to 15 nanometers: what PostGIS's
// ST_Distance and ST_DWithin measure between geography points. Longitudes
// may be any finite value; a latitude outside [-90, 90] or a coordinate
// that is not finite yields NaN.
func Distance(a, b Point) float64 {
	lat1, lon1, lat2, lon2 := a.Lat, a.Lon, b.Lat, b.Lon

	// lon12 in [0, 180] after the sign flip; lon12s its rounding error.
	lon12, lon12s := angDiff(lon1, lon2)
	if math.Signbit(lon12) {
		lon12, lon12s = -lon12, -lon12s
	}
	lam12 := lon12 * degree
	slam12, clam12 := sincosde(lon12, lon12s)
	lon12s = (180 - lon12) - lon12s // the supplementary longitude difference

	lat1 = angRound(latFix(lat1))
	lat2 = angRound(latFix(lat2))
	// Point 1 has the larger |latitude|, and a NaN latitude.
	if math.Abs(lat1) < math.Abs(lat2) || lat2 != lat2 {
		lat1, lat2 = lat2, lat1
	}
	// lat1 <= -0.
	if !math.Signbit(lat1) {
		lat1, lat2 = -lat1, -lat2
	}

	sbet1, cbet1 := sincosd(lat1)
	sbet1 *= f1
	sbet1, cbet1 = norm2(sbet1, cbet1)
	cbet1 = fmax(tiny, cbet1)

	sbet2, cbet2 := sincosd(lat2)
	sbet2 *= f1
	sbet2, cbet2 = norm2(sbet2, cbet2)
	cbet2 = fmax(tiny, cbet2)

	if cbet1 < -sbet1 {
		if cbet2 == cbet1 {
			sbet2 = math.Copysign(sbet1, sbet2)
		}
	} else if math.Abs(sbet2) == -sbet1 {
		cbet2 = cbet1
	}

	dn1 := math.Sqrt(1 + ep2*sq(sbet1))
	dn2 := math.Sqrt(1 + ep2*sq(sbet2))

	var ca [nC]float64
	var sig12, s12x, m12x float64
	meridian := lat1 == -90 || slam12 == 0

	if meridian {
		// Both points on one meridian: the geodesic may run along it.
		// Head to the target longitude; arrive heading north.
		calp1, calp2 := clam12, 1.0

		ssig1, csig1 := sbet1, calp1*cbet1
		ssig2, csig2 := sbet2, calp2*cbet2

		sig12 = math.Atan2(fmax(0.0, csig1*ssig2-ssig1*csig2)+0, csig1*csig2+ssig1*ssig2)
		s12x, m12x = lengths(n3, sig12, ssig1, csig1, dn1, ssig2, csig2, dn2, true, &ca)
		if sig12 < tol2 || m12x >= 0 {
			if sig12 < 3*tiny || (sig12 < tol0 && (s12x < 0 || m12x < 0)) {
				s12x = 0
			}
			s12x *= wgsB
		} else {
			// m12 < 0: prolate and too close to antipodal.
			meridian = false
		}
	}

	switch {
	case meridian:
	case sbet1 == 0 && lon12s >= wgsF*180:
		// Along the equator.
		s12x = wgsA * lam12
	default:
		// Within a hemisphere bounded by a meridian, neither meridional nor
		// equatorial: start Newton's method from InverseStart's estimate.
		var salp1, calp1, dnm float64
		sig12, salp1, calp1, dnm = inverseStart(sbet1, cbet1, sbet2, cbet2, lam12, slam12, clam12, &ca)

		if sig12 >= 0 {
			// Short lines.
			s12x = sig12 * wgsB * dnm
			break
		}
		var ssig1, csig1, ssig2, csig2, eps float64
		salp1a, calp1a, salp1b, calp1b := tiny, 1.0, tiny, -1.0
		tripn, tripb := false, false
		for numit := 0; ; numit++ {
			var v, dv float64
			v, sig12, ssig1, csig1, ssig2, csig2, eps, dv = lambda12(sbet1, cbet1, dn1, sbet2, cbet2, dn2,
				salp1, calp1, slam12, clam12, numit < maxit1, &ca)
			converged := 1.0
			if tripn {
				converged = 8
			}
			// The reversed test lets NaNs escape.
			if tripb || !(math.Abs(v) >= converged*tol0) || numit == maxit2 {
				break
			}
			if v > 0 && (numit > maxit1 || calp1/salp1 > calp1b/salp1b) {
				salp1b, calp1b = salp1, calp1
			} else if v < 0 && (numit > maxit1 || calp1/salp1 < calp1a/salp1a) {
				salp1a, calp1a = salp1, calp1
			}
			if numit < maxit1 && dv > 0 {
				dalp1 := -v / dv
				if math.Abs(dalp1) < math.Pi {
					sdalp1, cdalp1 := math.Sin(dalp1), math.Cos(dalp1)
					nsalp1 := salp1*cdalp1 + calp1*sdalp1
					if nsalp1 > 0 {
						calp1 = calp1*cdalp1 - salp1*sdalp1
						salp1 = nsalp1
						salp1, calp1 = norm2(salp1, calp1)
						tripn = math.Abs(v) <= 16*tol0
						continue
					}
				}
			}
			// dv was not positive or the step left (0, pi): bisect.
			salp1 = (salp1a + salp1b) / 2
			calp1 = (calp1a + calp1b) / 2
			salp1, calp1 = norm2(salp1, calp1)
			tripn = false
			tripb = math.Abs(salp1a-salp1)+(calp1a-calp1) < tolb || math.Abs(salp1-salp1b)+(calp1-calp1b) < tolb
		}
		s12x, _ = lengths(eps, sig12, ssig1, csig1, dn1, ssig2, csig2, dn2, true, &ca)
		s12x *= wgsB
	}
	return 0 + s12x // -0 → 0
}

// lengths returns s12b, the distance over b, when distance is set (else
// 0), and m12b, the reduced length over b.
func lengths(eps, sig12, ssig1, csig1, dn1, ssig2, csig2, dn2 float64, distance bool, ca *[nC]float64) (s12b, m12b float64) {
	var cb [nC]float64
	a1 := a1m1f(eps)
	c1f(eps, ca)
	a2 := a2m1f(eps)
	c2f(eps, &cb)
	m0 := a1 - a2
	a2 = 1 + a2
	a1 = 1 + a1

	var j12 float64
	if distance {
		b1 := sinCosSeries(true, ssig2, csig2, ca[:], order) - sinCosSeries(true, ssig1, csig1, ca[:], order)
		s12b = a1 * (sig12 + b1)
		b2 := sinCosSeries(true, ssig2, csig2, cb[:], order) - sinCosSeries(true, ssig1, csig1, cb[:], order)
		j12 = m0*sig12 + (a1*b1 - a2*b2)
	} else {
		for l := 1; l <= order; l++ {
			cb[l] = a1*ca[l] - a2*cb[l]
		}
		j12 = m0*sig12 + (sinCosSeries(true, ssig2, csig2, cb[:], order) - sinCosSeries(true, ssig1, csig1, cb[:], order))
	}
	// The parentheses keep cancellation exact for coincident points.
	m12b = dn2*(csig1*ssig2) - dn1*(ssig1*csig2) - csig1*csig2*j12
	return s12b, m12b
}

// inverseStart returns a starting point for Newton's method in salp1 and
// calp1 with sig12 = -1, or, for a short line that needs no iteration, its
// sig12 >= 0 and dnm.
func inverseStart(sbet1, cbet1, sbet2, cbet2, lam12, slam12, clam12 float64, ca *[nC]float64) (sig12, salp1, calp1, dnm float64) {
	sig12 = -1
	// bet12 = bet2 - bet1 in [0, pi); bet12a = bet2 + bet1 in (-pi, 0].
	sbet12 := sbet2*cbet1 - cbet2*sbet1
	cbet12 := cbet2*cbet1 + sbet2*sbet1
	shortline := cbet12 >= 0 && sbet12 < 0.5 && cbet2*lam12 < 0.5
	var somg12, comg12 float64
	sbet12a := sbet2*cbet1 + cbet2*sbet1
	if shortline {
		sbetm2 := sq(sbet1 + sbet2)
		sbetm2 /= sbetm2 + sq(cbet1+cbet2)
		dnm = math.Sqrt(1 + ep2*sbetm2)
		omg12 := lam12 / (f1 * dnm)
		somg12, comg12 = math.Sin(omg12), math.Cos(omg12)
	} else {
		somg12, comg12 = slam12, clam12
	}

	salp1 = cbet2 * somg12
	if comg12 >= 0 {
		calp1 = sbet12 + cbet2*sbet1*sq(somg12)/(1+comg12)
	} else {
		calp1 = sbet12a - cbet2*sbet1*sq(somg12)/(1-comg12)
	}

	ssig12 := math.Hypot(salp1, calp1)
	csig12 := sbet1*sbet2 + cbet1*cbet2*comg12

	switch {
	case shortline && ssig12 < etol2:
		// Really short lines.
		sig12 = math.Atan2(ssig12, csig12)
	case csig12 >= 0 || ssig12 >= 6*math.Abs(n3)*math.Pi*sq(cbet1):
		// The zeroth order spherical approximation is good enough.
	default:
		// Scale lam12 and bet2 to x, y, where the antipodal point is at the
		// origin and the singular point at y = 0, x = -1.
		lam12x := math.Atan2(-slam12, -clam12) // lam12 - pi
		k2 := sq(sbet1) * ep2
		eps := k2 / (2*(1+math.Sqrt(1+k2)) + k2)
		lamscale := wgsF * cbet1 * a3f(eps) * math.Pi
		betscale := lamscale * cbet1
		x := lam12x / lamscale
		y := sbet12a / betscale

		if y > -tol1 && x > -1-xthresh {
			// Strip near the cut.
			salp1 = fmin(1.0, -x)
			calp1 = -math.Sqrt(1 - sq(salp1))
		} else {
			// Estimate omg12 from the astroid problem and use the spherical
			// formula for alp1.
			k := astroid(x, y)
			omg12a := lamscale * (-x * k / (1 + k))
			somg12, comg12 = math.Sin(omg12a), -math.Cos(omg12a)
			salp1 = cbet2 * somg12
			calp1 = sbet12a - cbet2*sbet1*sq(somg12)/(1-comg12)
		}
	}
	// The backwards check lets NaN through.
	if !(salp1 <= 0) {
		salp1, calp1 = norm2(salp1, calp1)
	} else {
		salp1, calp1 = 1, 0
	}
	return sig12, salp1, calp1, dnm
}

// lambda12 returns v = lam12(alp1) - lam12 for Newton's method, the
// geodesic's sig12, the sines and cosines of sig1 and sig2, eps, and, when
// diffp, dv, the derivative of v.
func lambda12(sbet1, cbet1, dn1, sbet2, cbet2, dn2, salp1, calp1, slam120, clam120 float64, diffp bool, ca *[nC]float64) (
	v, sig12, ssig1, csig1, ssig2, csig2, eps, dv float64,
) {
	if sbet1 == 0 && calp1 == 0 {
		// Break the degeneracy of the equatorial line.
		calp1 = -tiny
	}

	salp0 := salp1 * cbet1
	calp0 := math.Hypot(calp1, salp1*sbet1)

	ssig1 = sbet1
	somg1 := salp0 * sbet1
	csig1 = calp1 * cbet1
	comg1 := csig1
	ssig1, csig1 = norm2(ssig1, csig1)

	// Enforce symmetries when |bet2| = -bet1.
	var calp2 float64
	if cbet2 != cbet1 || math.Abs(sbet2) != -sbet1 {
		var t float64
		if cbet1 < -sbet1 {
			t = (cbet2 - cbet1) * (cbet1 + cbet2)
		} else {
			t = (sbet1 - sbet2) * (sbet1 + sbet2)
		}
		calp2 = math.Sqrt(sq(calp1*cbet1)+t) / cbet2
	} else {
		calp2 = math.Abs(calp1)
	}

	ssig2 = sbet2
	somg2 := salp0 * sbet2
	csig2 = calp2 * cbet2
	comg2 := csig2
	ssig2, csig2 = norm2(ssig2, csig2)

	sig12 = math.Atan2(fmax(0.0, csig1*ssig2-ssig1*csig2)+0, csig1*csig2+ssig1*ssig2)

	somg12 := fmax(0.0, comg1*somg2-somg1*comg2) + 0
	comg12 := comg1*comg2 + somg1*somg2
	eta := math.Atan2(somg12*clam120-comg12*slam120, comg12*clam120+somg12*slam120)
	k2 := sq(calp0) * ep2
	eps = k2 / (2*(1+math.Sqrt(1+k2)) + k2)
	c3f(eps, ca)
	b312 := sinCosSeries(true, ssig2, csig2, ca[:], nC3-1) - sinCosSeries(true, ssig1, csig1, ca[:], nC3-1)
	domg12 := -wgsF * a3f(eps) * salp0 * (sig12 + b312)
	v = eta + domg12

	if diffp {
		if calp2 == 0 {
			dv = -2 * f1 * dn1 / sbet1
		} else {
			_, dv = lengths(eps, sig12, ssig1, csig1, dn1, ssig2, csig2, dn2, false, ca)
			dv *= f1 / (calp2 * cbet2)
		}
	}
	return v, sig12, ssig1, csig1, ssig2, csig2, eps, dv
}

// astroid solves k^4+2*k^3-(x^2+y^2-1)*k^2-2*y^2*k-y^2 = 0 for its
// positive root k.
func astroid(x, y float64) float64 {
	p := sq(x)
	q := sq(y)
	r := (p + q - 1) / 6
	if q == 0 && r <= 0 {
		// y = 0 with |x| <= 1.
		return 0
	}
	s := p * q / 4 // r^3 * s
	r2 := sq(r)
	r3 := r * r2
	// Zero on the evolute p^(1/3)+q^(1/3) = 1.
	disc := s * (s + 2*r3)
	u := r
	if disc >= 0 {
		t3 := s + r3
		// The sign of the root maximizes |t3|, minimizing cancellation.
		if t3 < 0 {
			t3 += -math.Sqrt(disc)
		} else {
			t3 += math.Sqrt(disc)
		}
		t := math.Cbrt(t3)
		if t != 0 {
			u += t + r2/t
		} else {
			u += t
		}
	} else {
		ang := math.Atan2(math.Sqrt(-disc), -(s + r3))
		// The cube root that avoids cancellation; disc < 0 implies r < 0.
		u += 2 * r * math.Cos(ang/3)
	}
	v := math.Sqrt(sq(u) + q)
	var uv float64
	if u < 0 {
		uv = q / (v - u)
	} else {
		uv = u + v
	}
	w := (uv - q) / (2 * v)
	return uv / (math.Sqrt(uv+sq(w)) + w)
}

// sinCosSeries evaluates sum(c[i] * sin(2*i*x), i, 1, n) when sinp, else
// sum(c[i] * cos((2*i+1)*x), i, 0, n-1), by Clenshaw summation.
func sinCosSeries(sinp bool, sinx, cosx float64, c []float64, n int) float64 {
	k := n // one beyond the last element
	if sinp {
		k++
	}
	ar := 2 * (cosx - sinx) * (cosx + sinx) // 2 * cos(2 * x)
	var y0, y1 float64
	if n&1 != 0 {
		k--
		y0 = c[k]
	}
	for n /= 2; n > 0; n-- {
		k--
		y1 = ar*y0 - y1 + c[k]
		k--
		y0 = ar*y1 - y0 + c[k]
	}
	if sinp {
		return 2 * sinx * cosx * y0 // sin(2 * x) * y0
	}
	return cosx * (y0 - y1) // cos(x) * (y0 - y1)
}

// a3f is A3, the mean value of (d/dsigma)I3.
func a3f(eps float64) float64 { return polyval(order-1, a3x[:], eps) }

// c3f sets c[1] through c[nC3-1] to the C3 coefficients.
func c3f(eps float64, c *[nC]float64) {
	mult := 1.0
	o := 0
	for l := 1; l < nC3; l++ {
		m := nC3 - l - 1 // the order of the polynomial in eps
		mult *= eps
		c[l] = mult * polyval(m, c3x[o:], eps)
		o += m + 1
	}
}

// a1m1f is (1-eps)*A1-1.
func a1m1f(eps float64) float64 {
	coeff := [...]float64{1, 4, 64, 0, 256}
	const m = order / 2
	t := polyval(m, coeff[:], sq(eps)) / coeff[m+1]
	return (t + eps) / (1 - eps)
}

// c1f sets c[1] through c[order] to the C1 coefficients of B1.
func c1f(eps float64, c *[nC]float64) {
	coeff := [...]float64{
		-1, 6, -16, 32,
		-9, 64, -128, 2048,
		9, -16, 768,
		3, -5, 512,
		-7, 1280,
		-7, 2048,
	}
	eps2 := sq(eps)
	d := eps
	o := 0
	for l := 1; l <= order; l++ {
		m := (order - l) / 2 // the order of the polynomial in eps^2
		c[l] = d * polyval(m, coeff[o:], eps2) / coeff[o+m+1]
		o += m + 2
		d *= eps
	}
}

// a2m1f is (1+eps)*A2-1.
func a2m1f(eps float64) float64 {
	coeff := [...]float64{-11, -28, -192, 0, 256}
	const m = order / 2
	t := polyval(m, coeff[:], sq(eps)) / coeff[m+1]
	return (t - eps) / (1 + eps)
}

// c2f sets c[1] through c[order] to the C2 coefficients of B2.
func c2f(eps float64, c *[nC]float64) {
	coeff := [...]float64{
		1, 2, 16, 32,
		35, 64, 384, 2048,
		15, 80, 768,
		7, 35, 512,
		63, 1280,
		77, 2048,
	}
	eps2 := sq(eps)
	d := eps
	o := 0
	for l := 1; l <= order; l++ {
		m := (order - l) / 2
		c[l] = d * polyval(m, coeff[o:], eps2) / coeff[o+m+1]
		o += m + 2
		d *= eps
	}
}

// a3coeff is A3's coefficients in eps, from the highest power down, for
// WGS 84's n.
func a3coeff() [order]float64 {
	coeff := [...]float64{
		-3, 128,
		-2, -3, 64,
		-1, -3, -1, 16,
		3, -1, -2, 8,
		1, -1, 2,
		1, 1,
	}
	var out [order]float64
	o, k := 0, 0
	for j := order - 1; j >= 0; j-- { // the coefficient of eps^j
		m := min(order-j-1, j) // the order of the polynomial in n
		out[k] = polyval(m, coeff[o:], n3) / coeff[o+m+1]
		k++
		o += m + 2
	}
	return out
}

// c3coeff is the C3 coefficients' polynomials in eps for WGS 84's n.
func c3coeff() [(nC3 * (nC3 - 1)) / 2]float64 {
	coeff := [...]float64{
		3, 128,
		2, 5, 128,
		-1, 3, 3, 64,
		-1, 0, 1, 8,
		-1, 1, 4,
		5, 256,
		1, 3, 128,
		-3, -2, 3, 64,
		1, -3, 2, 32,
		7, 512,
		-10, 9, 384,
		5, -9, 5, 192,
		7, 512,
		-14, 7, 512,
		21, 2560,
	}
	var out [(nC3 * (nC3 - 1)) / 2]float64
	o, k := 0, 0
	for l := 1; l < nC3; l++ {
		for j := nC3 - 1; j >= l; j-- {
			m := min(nC3-j-1, j)
			out[k] = polyval(m, coeff[o:], n3) / coeff[o+m+1]
			k++
			o += m + 2
		}
	}
	return out
}

// polyval evaluates the polynomial of order n with coefficients p, highest
// power first, at x.
func polyval(n int, p []float64, x float64) float64 {
	if n < 0 {
		return 0
	}
	y := p[0]
	for i := 1; i <= n; i++ {
		y = y*x + p[i]
	}
	return y
}

func sq(x float64) float64 { return x * x }

// sumx is the error-free sum: u + v = s + t, s = round(u + v).
func sumx(u, v float64) (s, t float64) {
	s = u + v
	up := s - v
	vpp := s - up
	up -= u
	vpp -= v
	if s != 0 {
		t = 0 - (up + vpp)
	} else {
		t = s
	}
	return s, t
}

func norm2(sinx, cosx float64) (float64, float64) {
	r := math.Hypot(sinx, cosx)
	return sinx / r, cosx / r
}

func latFix(x float64) float64 {
	if math.Abs(x) > 90 {
		return math.NaN()
	}
	return x
}

// angDiff is y - x reduced to [-180, 180] and its rounding error.
func angDiff(x, y float64) (d, e float64) {
	d, t := sumx(math.Remainder(-x, 360), math.Remainder(y, 360))
	d, t = sumx(math.Remainder(d, 360), t)
	// Fix the sign when d is -180, 0 or 180.
	if d == 0 || math.Abs(d) == 180 {
		if t == 0 {
			d = math.Copysign(d, y-x)
		} else {
			d = math.Copysign(d, -t)
		}
	}
	return d, t
}

// angRound rounds tiny angles to a multiple of 2^-57 so that a point very
// near the equator counts as on it.
func angRound(x float64) float64 {
	const z = 1.0 / 16
	y := math.Abs(x)
	w := z - y
	if w > 0 {
		y = z - w
	}
	return math.Copysign(y, x)
}

// remquo90 is C's remquo(x, 90): the remainder in [-45, 45] and the
// quotient, whose low bits pick the quadrant.
func remquo90(x float64) (float64, int) {
	r := math.Remainder(x, 90)
	return r, int(math.Round((x - r) / 90))
}

// sincosd is the sine and cosine of x degrees, reducing x exactly to
// [-45, 45] before converting it to radians.
func sincosd(x float64) (float64, float64) {
	r, q := remquo90(x)
	return quadrant(r*degree, q, x)
}

// sincosde is sincosd of x + t, t a small correction to x.
func sincosde(x, t float64) (float64, float64) {
	r, q := remquo90(x)
	return quadrant(angRound(r+t)*degree, q, x)
}

func quadrant(r float64, q int, x float64) (sinx, cosx float64) {
	s, c := math.Sin(r), math.Cos(r)
	switch uint(q) & 3 {
	case 0:
		sinx, cosx = s, c
	case 1:
		sinx, cosx = c, -s
	case 2:
		sinx, cosx = -s, -c
	default:
		sinx, cosx = -c, s
	}
	cosx += 0
	if sinx == 0 {
		sinx = math.Copysign(sinx, x)
	}
	return sinx, cosx
}

// fmax and fmin are C's: a NaN operand yields the other.
func fmax(x, y float64) float64 {
	switch {
	case x != x:
		return y
	case y != y:
		return x
	}
	return math.Max(x, y)
}

func fmin(x, y float64) float64 {
	switch {
	case x != x:
		return y
	case y != y:
		return x
	}
	return math.Min(x, y)
}
