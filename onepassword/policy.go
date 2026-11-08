package onepassword

import (
	"errors"
	"time"
)

// Policy bounds complete read-only native observations and exact selected-ID batches.
type Policy struct {
	ListTimeout    time.Duration `koanf:"list_timeout" default:"3m" validate:"gt=0" desc:"Complete metadata observation and native unlock allowance"`
	ItemTimeout    time.Duration `koanf:"item_timeout" default:"30s" validate:"gt=0" desc:"Selected item batch or native reference observation deadline"`
	MaxItems       int           `koanf:"max_items" default:"10000" validate:"gt=0,lte=100000" desc:"Complete vault metadata and selected item bound"`
	MaxFields      int           `koanf:"max_fields" default:"1000" validate:"gt=0,lte=10000" desc:"Complete native fields per item bound"`
	BatchSize      int           `koanf:"batch_size" default:"32" validate:"gt=0,lte=128" desc:"Selected item IDs per native batch"`
	MaxNativeBytes int           `koanf:"max_native_bytes" default:"16777216" validate:"gt=0,lte=67108864" desc:"Native input/reference, each captured stream and aggregate selected-item response byte bound"`
}

// DefaultPolicy supplies the shared configuration schema defaults.
func DefaultPolicy() Policy {
	return Policy{ListTimeout: 3 * time.Minute, ItemTimeout: 30 * time.Second, MaxItems: 10000, MaxFields: 1000, BatchSize: 32, MaxNativeBytes: 16 << 20}
}

// Validate rejects unbounded native deadlines, coverage, batches and byte limits.
func (p Policy) Validate() error {
	if p.ListTimeout <= 0 || p.ItemTimeout <= 0 || p.MaxItems < 1 || p.MaxItems > 100000 || p.MaxFields < 1 || p.MaxFields > 10000 || p.BatchSize < 1 || p.BatchSize > 128 || p.MaxNativeBytes < 1 || p.MaxNativeBytes > 64<<20 {
		return errors.New("native observations require positive bounded deadlines, coverage, batches and bytes")
	}
	return nil
}
