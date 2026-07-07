// Package output is the single output engine used by every command (spec §5).
// stdout gets data only; formats: table (TTY default), json, jsonl
// (piped streaming default), raw, csv.
package output

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

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
}

func NewPrinter(w io.Writer, f Format) *Printer { return &Printer{w: w, format: f} }

func cell(row map[string]any, col string) string {
	v, ok := row[col]
	if !ok || v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

func (p *Printer) PrintRows(columns []string, rows []map[string]any) error {
	switch p.format {
	case FormatJSON:
		if rows == nil {
			rows = []map[string]any{}
		}
		enc := json.NewEncoder(p.w)
		enc.SetIndent("", "  ")
		return enc.Encode(rows)
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
	switch p.format {
	case FormatRaw:
		if raw, ok := row["@raw"]; ok {
			_, err := fmt.Fprintln(p.w, raw)
			return err
		}
		return json.NewEncoder(p.w).Encode(row)
	case FormatJSONL:
		return json.NewEncoder(p.w).Encode(row)
	default:
		return clierr.New("internal_output_misuse",
			fmt.Sprintf("PrintRow requires a streaming format, got %q", p.format))
	}
}

func (p *Printer) PrintJSON(v any) error {
	enc := json.NewEncoder(p.w)
	if p.format == FormatTable { // human context: pretty-print
		enc.SetIndent("", "  ")
	}
	return enc.Encode(v)
}
