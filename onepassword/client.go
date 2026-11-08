// Package onepassword owns bounded read-only native CLI observations.
package onepassword

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/ashtonian/pkg/process"
)

// Runner must supervise command descendants and bound each captured output stream.
type Runner interface {
	Run(context.Context, process.Command) (process.Result, error)
}

// Client owns read-only native metadata and exact selected-ID batch observations.
type Client struct {
	runner      Runner
	environment []string
	account     string
	executable  string
	policy      Policy
}

// Option configures a native observation dependency or bound.
type Option func(*Client)

// WithRunner supplies the bounded process owner.
func WithRunner(runner Runner) Option { return func(c *Client) { c.runner = runner } }

// WithExecutable selects the installed native 1Password CLI.
func WithExecutable(value string) Option { return func(c *Client) { c.executable = value } }

// WithPolicy selects the complete observation bounds.
func WithPolicy(policy Policy) Option { return func(c *Client) { c.policy = policy } }

// New validates dependencies before any vault access.
func New(options ...Option) (*Client, error) {
	c := &Client{executable: "op", policy: DefaultPolicy()}
	for _, option := range options {
		option(c)
	}
	if c.runner == nil || c.executable == "" || c.account != "" && !validLabel(c.account) {
		return nil, errors.New("native 1Password requires a bounded native runner and executable")
	}
	if err := c.policy.Validate(); err != nil {
		return nil, err
	}
	return c, nil
}

// ItemMetadata binds an item title/version to its native vault and item IDs.
type ItemMetadata struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Version *int   `json:"version"`
	Vault   Vault  `json:"vault"`
}

// Field retains native field identity, location and value; callers must not log it.
type Field struct {
	ID        string   `json:"id"`
	Type      string   `json:"type"`
	Reference string   `json:"reference"`
	Section   *Section `json:"section,omitempty"`
	Label     string   `json:"label"`
	Value     string   `json:"value"`
}

// Item contains exact observed metadata and sensitive native field values.
type Item struct {
	ItemMetadata
	Fields   []Field   `json:"fields"`
	Files    []File    `json:"files"`
	Sections []Section `json:"sections"`
}

func completeJSON(decoder *json.Decoder) bool {
	return errors.Is(decoder.Decode(new(any)), io.EOF)
}

func (c *Client) call(ctx context.Context, stage string, timeout time.Duration, args []string, input []byte) ([]byte, error) {
	if len(input) > c.policy.MaxNativeBytes {
		return nil, errors.New("native vault selected-ID input exceeds its byte bound")
	}
	work, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := work.Err(); err != nil {
		return nil, err
	}
	if c.account != "" {
		args = append([]string{"--account", c.account}, args...)
	}
	result, err := c.runner.Run(work, process.Command{Name: c.executable, Args: args, Input: input, Env: c.environment})
	if err != nil {
		// Native diagnostics can contain secret values; expose only bounded failure classes.
		failure := fmt.Errorf("native vault %s failed; verify op authentication and vault access", stage)
		if errors.Is(err, context.Canceled) {
			return nil, errors.Join(failure, context.Canceled)
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, errors.Join(failure, context.DeadlineExceeded)
		}
		if errors.Is(err, process.ErrUnsupportedPlatform) {
			return nil, errors.Join(failure, process.ErrUnsupportedPlatform)
		}
		if errors.Is(err, process.ErrOutputLimit) {
			return nil, errors.Join(failure, process.ErrOutputLimit)
		}
		return nil, errors.Join(failure, work.Err())
	}
	if len(result.Stdout) > c.policy.MaxNativeBytes || len(result.Stderr) > c.policy.MaxNativeBytes {
		return nil, process.ErrOutputLimit
	}
	if err := work.Err(); err != nil {
		return nil, err
	}
	return result.Stdout, nil
}

// List observes complete metadata for one explicit vault.
func (c *Client) List(ctx context.Context, vault string) ([]ItemMetadata, error) {
	if !validLabel(vault) {
		return nil, errors.New("native metadata requires an exact vault name or ID")
	}
	data, err := c.call(ctx, "metadata observation", c.policy.ListTimeout, []string{"item", "list", "--vault", vault, "--format", "json"}, nil)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('[') {
		return nil, errors.New("native vault metadata is not a complete item array")
	}
	items := []ItemMetadata{}
	seen := map[string]bool{}
	vaultID := ""
	for decoder.More() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if len(items) >= c.policy.MaxItems {
			return nil, errors.New("native vault metadata exceeds the complete item bound")
		}
		var item ItemMetadata
		if err := decoder.Decode(&item); err != nil || !validID(item.ID) || item.Title == "" || !validID(item.Vault.ID) || seen[item.ID] || vaultID != "" && item.Vault.ID != vaultID || item.Version != nil && *item.Version < 1 {
			return nil, errors.New("native vault metadata has malformed, duplicate or conflicting identities")
		}
		seen[item.ID], vaultID = true, item.Vault.ID
		items = append(items, item)
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim(']') || !completeJSON(decoder) {
		return nil, errors.New("native vault metadata is incomplete or has trailing data")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func decodeRows[T any](ctx context.Context, raw json.RawMessage, maximum int) ([]T, error) {
	result := []T{}
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return result, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if token, err := decoder.Token(); err != nil || token != json.Delim('[') {
		return nil, errors.New("native item rows are not an array")
	}
	for decoder.More() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if len(result) >= maximum {
			return nil, errors.New("native item exceeds the complete field bound")
		}
		var row T
		if err := decoder.Decode(&row); err != nil {
			return nil, errors.New("native item row is not supported JSON")
		}
		result = append(result, row)
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim(']') || !completeJSON(decoder) {
		return nil, errors.New("native item row array is incomplete")
	}
	return result, nil
}

func decodeNativeItem(ctx context.Context, decoder *json.Decoder, maximum int) (Item, error) {
	var raw struct {
		ItemMetadata
		Fields   json.RawMessage `json:"fields"`
		Files    json.RawMessage `json:"files"`
		Sections json.RawMessage `json:"sections"`
	}
	if err := decoder.Decode(&raw); err != nil {
		return Item{}, errors.New("native item is not complete supported JSON")
	}
	fields, err := decodeRows[Field](ctx, raw.Fields, maximum)
	if err != nil {
		return Item{}, err
	}
	files, err := decodeRows[File](ctx, raw.Files, maximum)
	if err != nil {
		return Item{}, err
	}
	sections, err := decodeRows[Section](ctx, raw.Sections, maximum)
	if err != nil {
		return Item{}, err
	}
	return Item{ItemMetadata: raw.ItemMetadata, Fields: fields, Files: files, Sections: sections}, nil
}

// Get reads exact selected metadata identities in bounded native batches.
func (c *Client) Get(ctx context.Context, vault string, selected []ItemMetadata) (map[string]Item, error) {
	if !validLabel(vault) || len(selected) > c.policy.MaxItems {
		return nil, errors.New("native item selection lacks exact bounded vault scope")
	}
	seen := map[string]bool{}
	vaultID := ""
	for _, item := range selected {
		if !validID(item.ID) || !validID(item.Vault.ID) || !validLabel(item.Title) || seen[item.ID] || vaultID != "" && vaultID != item.Vault.ID || item.Version != nil && *item.Version < 1 {
			return nil, errors.New("native item selection has invalid or repeated identities")
		}
		seen[item.ID] = true
		vaultID = item.Vault.ID
	}
	result := map[string]Item{}
	for batch := range slices.Chunk(selected, c.policy.BatchSize) {
		input := make([]map[string]string, len(batch))
		expected := map[string]ItemMetadata{}
		for i, item := range batch {
			input[i] = map[string]string{"id": item.ID}
			expected[item.ID] = item
		}
		payload, err := json.Marshal(input)
		if err != nil {
			return nil, err
		}
		data, err := c.call(ctx, "selected item observation", c.policy.ItemTimeout, []string{"item", "get", "-", "--vault", vault, "--format", "json", "--reveal"}, payload)
		if err != nil {
			return nil, err
		}
		data = bytes.TrimSpace(data)
		if len(data) == 0 {
			return nil, errors.New("native vault item batch is empty")
		}
		decoder := json.NewDecoder(bytes.NewReader(data))
		array := data[0] == '['
		if array {
			if _, err := decoder.Token(); err != nil {
				return nil, errors.New("native vault item batch is malformed")
			}
		}
		count := 0
		for count < len(batch) {
			if array && !decoder.More() {
				break
			}
			item, err := decodeNativeItem(ctx, decoder, c.policy.MaxFields)
			if err != nil {
				return nil, err
			}
			before, exists := expected[item.ID]
			if !exists || item.Title != before.Title || item.Vault.ID != before.Vault.ID || item.Version != nil && *item.Version < 1 || before.Version != nil && (item.Version == nil || *item.Version != *before.Version) {
				return nil, errors.New("native vault item identity changed or is outside the selected batch")
			}
			if _, exists := result[item.ID]; exists {
				return nil, errors.New("native vault item batch repeats an observed identity")
			}
			result[item.ID] = item
			count++
		}
		if array {
			if token, err := decoder.Token(); err != nil || token != json.Delim(']') {
				return nil, errors.New("native vault item batch is incomplete")
			}
		}
		if count != len(batch) || !completeJSON(decoder) {
			return nil, errors.New("native vault item batch lacks complete exact selected-ID coverage")
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

// Section identifies a native item's optional field section.
type Section struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

func validLabel(value string) bool {
	return strings.TrimSpace(value) != "" && len(value) <= 1024 && !strings.ContainsFunc(value, unicode.IsControl)
}
func validID(value string) bool {
	if value == "" || len(value) > 1024 {
		return false
	}
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			continue
		}
		return false
	}
	return true
}

// WithEnvironment supplies a complete native child environment; nil inherits the caller's environment.
func WithEnvironment(value []string) Option {
	return func(c *Client) { c.environment = slices.Clone(value) }
}

// WithAccount selects a native account name or ID; empty retains native account selection.
func WithAccount(value string) Option { return func(c *Client) { c.account = value } }

// Read resolves one operator-approved native reference without trimming or interpreting its bytes.
// Field-level reads preserve native binary/format/query semantics; callers should batch ordinary item observations with Get.
func (c *Client) Read(ctx context.Context, reference string) ([]byte, error) {
	if !strings.HasPrefix(reference, "op://") || len(reference) <= len("op://") || len(reference) > c.policy.MaxNativeBytes || strings.ContainsFunc(reference, unicode.IsControl) {
		return nil, errors.New("native read requires a bounded explicit op reference")
	}
	return c.call(ctx, "reference observation", c.policy.ItemTimeout, []string{"read", "--no-newline", reference}, nil)
}

// Vault identifies the native vault selected by a complete observation.
type Vault struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// File retains attachment metadata; content is resolved only by an explicit Read.
type File struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Size      int64    `json:"size,omitempty"`
	Reference string   `json:"reference,omitempty"`
	Section   *Section `json:"section,omitempty"`
}
