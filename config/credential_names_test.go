package config

import (
	"reflect"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsSensitiveName(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"Password", true},
		{"Passwords", true},
		{"DBPassword", true},
		{"dbpassword", true},
		{"db_password", true},
		{"Passwd", true},
		{"SMTPPwd", true},
		{"RedisPass", true},
		{"Passphrase", true},
		{"ClientSecret", true},
		{"AccessToken", true},
		{"accesstoken", true},
		{"Tokens", true},
		{"KeyPath", true},
		{"JWTKey", true},
		{"AWSAccessKeyID", true},
		{"MailAPIKey", true},
		{"APIKEY", true},
		{"x-api-key", true},
		{"SigningKeys", true},
		{"Credential", true},
		{"Creds", true},
		{"SMSAuth", true},
		{"OAuth2Token", true},
		{"GatewayAuthorization", true},
		{"ErrorDSN", true},
		{"PrivateKey", true},
		{"secretkey", true},
		{"awsaccesskey", true},
		{"privatekeys", true},

		{"Keyspace", false},
		{"Tokenizer", false},
		{"Monkey", false},
		{"Hotkey", false},
		{"Bypass", false},
		{"AuthenticationMode", false},
		{"Host", false},
		{"Port", false},
		{"Address", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isSensitiveName(tt.name))
		})
	}
}

func TestNameWords(t *testing.T) {
	tests := []struct {
		name string
		want []string
	}{
		{"HTTPAuthMode", []string{"HTTP", "Auth", "Mode"}},
		{"APIKey", []string{"API", "Key"}},
		{"APIKEY", []string{"APIKEY"}},
		{"AWSAccessKeyID", []string{"AWS", "Access", "Key", "ID"}},
		{"db_password", []string{"db", "password"}},
		{"x-api-key", []string{"x", "api", "key"}},
		{"S3Secret", []string{"S", "3", "Secret"}},
		{"OAuth2Token", []string{"O", "Auth", "2", "Token"}},
		{"Keyspace", []string{"Keyspace"}},
		{"ÜberKey", []string{"Über", "Key"}},
		{"__", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, slices.Collect(nameWords(tt.name)))
		})
	}
}

// Judging a name allocates nothing: map keys are judged on every diff and
// redacted print.
func TestIsSensitiveName_DoesNotAllocate(t *testing.T) {
	for _, name := range []string{"AWSAccessKeyID", "Keyspace", "db_password", "ÜberKey"} {
		assert.Zero(t, testing.AllocsPerRun(100, func() { isSensitiveName(name) }), name)
	}
}

// A field whose name only contains a credential word's letters keeps its
// value in every redacted output; one whose words include it does not.
func TestRedactedMap_JudgesFieldNamesByWords(t *testing.T) {
	type cfg struct {
		Keyspace  string `koanf:"keyspace"`
		Tokenizer string `koanf:"tokenizer"`
		APIKey    string `koanf:"api_key"`
	}
	got := RedactedMap(cfg{Keyspace: "events", Tokenizer: "unicode", APIKey: "k-123"})
	assert.Equal(t, map[string]string{"keyspace": "events", "tokenizer": "unicode", "api_key": redactedValue}, got)
}

// Both credential sets are fixed-size arrays: a slice could be appended to or
// resliced at run time, changing what is redacted for every caller.
func TestCredentialSetsAreArrays(t *testing.T) {
	assert.Equal(t, reflect.Array, reflect.TypeOf(credentialWords).Kind())
	assert.Equal(t, reflect.Array, reflect.TypeOf(sensitivePatterns).Kind())
}
