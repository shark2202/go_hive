// Package excel owns the Excel integration end to end: the upstream catalog
// (one "excel" tool with six actions), the OOXML workbook reader/writer, the
// action implementations and their tests.
//
// It is the pilot for the domain-subpackage structure described in
// docs/integrations-package-structure.md:
//
//   - Catalog/NewRegistry/Register are the only exported entry points the root
//     assembly needs.
//   - The package imports internal/tools/integrationkit and the standard
//     library; it never imports the integrations root or internal/tools.
//   - The historical batch3* identifiers were dropped on the way in; the
//     behavior and published schema are unchanged (see the catalog digest test).
//
// Parity: tools/src/aden_tools/tools/excel_tool/excel_tool.py
package excel
