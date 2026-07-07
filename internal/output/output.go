// Package output is the single output engine used by every command (spec §5).
// stdout gets data only; formats: table (TTY default), json, jsonl
// (piped streaming default), raw, csv.
package output

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/itchyny/gojq"

	"github.com/svrnm/bronto-cli/internal/clierr"
)

type Format string

const (
	FormatTable Format = "table"
	FormatJSON  Format = "json"
	FormatJSONL Format = "jsonl"
	FormatRaw   Format = "raw"
	FormatCSV   Format = "csv"
)

func ParseFormat(s string) (Format, error) {
	switch Format(s) {
	case FormatTable, FormatJSON, FormatJSONL, FormatRaw, FormatCSV:
		return Format(s), nil
	}
	return "", clierr.New("usage_invalid_output_format",
		fmt.Sprintf("unknown output format %q", s)).
		WithHint("Valid formats: table, json, jsonl, raw, csv.")
}

func DetectFormat(flagVal string, stdoutIsTTY, streaming bool) (Format, error) {
	if flagVal != "" {
		return ParseFormat(flagVal)
	}
	if stdoutIsTTY {
		return FormatTable, nil
	}
	if streaming {
		return FormatJSONL, nil
	}
	return FormatJSON, nil
}

type Printer struct {
	w      io.Writer
	format Format

	fields     []string // SetFieldFilter: table/csv column override, json/jsonl key filter
	jq         *gojq.Code
	listFields bool                // SetListFields: print field names instead of data
	seenFields map[string]struct{} // streaming PrintRow "?" mode: keys already printed
}

func NewPrinter(w io.Writer, f Format) *Printer { return &Printer{w: w, format: f} }

// SetFieldFilter restricts output to the given fields. For table/csv the
// fields become the columns, overriding whatever columns the caller passed
// to PrintRows. For json/jsonl each row is filtered down to those keys.
// Applied before any jq expression (SetJQ).
func (p *Printer) SetFieldFilter(fields []string) { p.fields = fields }

// SetJQ makes json/jsonl output run every emitted document through code,
// printing each result gojq yields as its own compact JSON line (jq
// semantics). For PrintRows/PrintRow that means one document per row; for
// PrintJSON the whole payload is the document. Callers are responsible for
// rejecting this combination with table/csv/raw formats (spec: --jq
// requires -o json or jsonl).
func (p *Printer) SetJQ(code *gojq.Code) { p.jq = code }

// SetListFields switches PrintRows/PrintRow into "--fields ?" mode: instead
// of data, they print the sorted union of row keys, one per line.
// PrintRows sees every row up front and prints one pass; streaming
// PrintRow prints newly-seen keys as they appear across calls.
func (p *Printer) SetListFields(v bool) { p.listFields = v }

func cell(row map[string]any, col string) string {
	v, ok := row[col]
	if !ok || v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

func filterRow(row map[string]any, fields []string) map[string]any {
	out := make(map[string]any, len(fields))
	for _, f := range fields {
		if v, ok := row[f]; ok {
			out[f] = v
		}
	}
	return out
}

func filterRows(rows []map[string]any, fields []string) []map[string]any {
	out := make([]map[string]any, len(rows))
	for i, r := range rows {
		out[i] = filterRow(r, fields)
	}
	return out
}

// printFieldUnion implements "--fields ?" for PrintRows: the sorted union
// of every row's keys, one per line, in place of the data.
func (p *Printer) printFieldUnion(rows []map[string]any) error {
	seen := map[string]struct{}{}
	for _, r := range rows {
		for k := range r {
			seen[k] = struct{}{}
		}
	}
	names := make([]string, 0, len(seen))
	for k := range seen {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, n := range names {
		if _, err := fmt.Fprintln(p.w, n); err != nil {
			return err
		}
	}
	return nil
}

// printNewFields implements "--fields ?" for streaming PrintRow: rows
// arrive one at a time, so the union can't be computed ahead of time. Each
// call prints only the keys not already seen in a previous call.
func (p *Printer) printNewFields(row map[string]any) error {
	if p.seenFields == nil {
		p.seenFields = map[string]struct{}{}
	}
	var keys []string
	for k := range row {
		if _, ok := p.seenFields[k]; !ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		p.seenFields[k] = struct{}{}
		if _, err := fmt.Fprintln(p.w, k); err != nil {
			return err
		}
	}
	return nil
}

func (p *Printer) PrintRows(columns []string, rows []map[string]any) error {
	if p.listFields {
		return p.printFieldUnion(rows)
	}
	if len(p.fields) > 0 {
		columns = p.fields
	}
	switch p.format {
	case FormatJSON:
		filtered := rows
		if len(p.fields) > 0 {
			filtered = filterRows(rows, p.fields)
		}
		if filtered == nil {
			filtered = []map[string]any{}
		}
		if p.jq != nil {
			for _, r := range filtered {
				if err := runJQ(p.w, p.jq, r); err != nil {
					return err
				}
			}
			return nil
		}
		enc := json.NewEncoder(p.w)
		enc.SetIndent("", "  ")
		return enc.Encode(filtered)
	case FormatJSONL, FormatRaw:
		for _, r := range rows {
			if err := p.PrintRow(columns, r); err != nil {
				return err
			}
		}
		return nil
	case FormatCSV:
		cw := csv.NewWriter(p.w)
		if err := cw.Write(columns); err != nil {
			return err
		}
		for _, r := range rows {
			rec := make([]string, len(columns))
			for i, c := range columns {
				rec[i] = cell(r, c)
			}
			if err := cw.Write(rec); err != nil {
				return err
			}
		}
		cw.Flush()
		return cw.Error()
	default: // table
		tw := tabwriter.NewWriter(p.w, 2, 4, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, strings.ToUpper(strings.Join(columns, "\t")))
		for _, r := range rows {
			vals := make([]string, len(columns))
			for i, c := range columns {
				vals[i] = cell(r, c)
			}
			_, _ = fmt.Fprintln(tw, strings.Join(vals, "\t"))
		}
		return tw.Flush()
	}
}

func (p *Printer) PrintRow(columns []string, row map[string]any) error {
	if p.listFields {
		return p.printNewFields(row)
	}
	switch p.format {
	case FormatRaw:
		if raw, ok := row["@raw"]; ok {
			_, err := fmt.Fprintln(p.w, raw)
			return err
		}
		return json.NewEncoder(p.w).Encode(row)
	case FormatJSONL:
		r := row
		if len(p.fields) > 0 {
			r = filterRow(row, p.fields)
		}
		if p.jq != nil {
			return runJQ(p.w, p.jq, r)
		}
		return json.NewEncoder(p.w).Encode(r)
	default:
		return clierr.New("internal_output_misuse",
			fmt.Sprintf("PrintRow requires a streaming format, got %q", p.format))
	}
}

func (p *Printer) PrintJSON(v any) error {
	if p.jq != nil {
		return runJQ(p.w, p.jq, v)
	}
	enc := json.NewEncoder(p.w)
	if p.format == FormatTable { // human context: pretty-print
		enc.SetIndent("", "  ")
	}
	return enc.Encode(v)
}
