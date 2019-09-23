package structfilter

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/vmihailenco/tagparser"

	"github.com/go-pg/pg/v9/internal"
	"github.com/go-pg/pg/v9/types"
	"github.com/go-pg/zerochecker"
)

type opCode int

const (
	opCodeEq opCode = iota + 1
	opCodeNotEq
	opCodeLT
	opCodeLTE
	opCodeGT
	opCodeGTE
	opCodeIEq
	opCodeMatch
)

var (
	opEq    = " = "
	opNotEq = " != "
	opLT    = " < "
	opLTE   = " <= "
	opGT    = " > "
	opGTE   = " >= "
	opAny   = " = ANY"
	opAll   = " != ALL"
	opIEq   = " ILIKE "
	opMatch = " SIMILAR TO "
)

type Field struct {
	name   string
	index  []int
	Column string

	opCode  opCode
	OpValue string

	IsSlice  bool
	noDecode bool
	required bool
	noWhere  bool

	ScanValue   ScanFunc
	AppendValue types.AppenderFunc
	isZeroValue zerochecker.Func
}

func newField(sf reflect.StructField) *Field {
	f := &Field{
		name:    sf.Name,
		index:   sf.Index,
		IsSlice: sf.Type.Kind() == reflect.Slice,
	}

	pgTag := tagparser.Parse(sf.Tag.Get("pg"))
	if pgTag.Name == "-" {
		return nil
	}
	if pgTag.Name != "" {
		f.name = pgTag.Name
	}

	_, f.required = pgTag.Options["required"]
	_, f.noDecode = pgTag.Options["nodecode"]
	_, f.noWhere = pgTag.Options["nowhere"]
	if f.required && f.noWhere {
		err := fmt.Errorf("pg: required and nowhere tags can't be set together")
		panic(err)
	}

	name := internal.Underscore(f.name)
	const sep = "_"

	if f.IsSlice {
		f.Column, f.opCode, f.OpValue = splitSliceColumnOperator(name, sep)
		f.ScanValue = arrayScanner(sf.Type)
		f.AppendValue = types.ArrayAppender(sf.Type)
	} else {
		f.Column, f.opCode, f.OpValue = splitColumnOperator(name, sep)
		f.ScanValue = scanner(sf.Type)
		f.AppendValue = types.Appender(sf.Type)
	}
	f.isZeroValue = zerochecker.Checker(sf.Type)

	if f.ScanValue == nil || f.AppendValue == nil {
		return nil
	}

	return f
}

func (f *Field) NoDecode() bool {
	return f.noDecode
}

func (f *Field) Value(strct reflect.Value) reflect.Value {
	return strct.FieldByIndex(f.index)
}

func (f *Field) Omit(value reflect.Value) bool {
	return !f.required && f.noWhere || f.isZeroValue(value)
}

func splitColumnOperator(s, sep string) (string, opCode, string) {
	ind := strings.LastIndex(s, sep)
	if ind == -1 {
		return s, opCodeEq, opEq
	}

	col := s[:ind]
	op := s[ind+len(sep):]

	switch op {
	case "eq", "":
		return col, opCodeEq, opEq
	case "neq", "exclude":
		return col, opCodeNotEq, opNotEq
	case "gt":
		return col, opCodeGT, opGT
	case "gte":
		return col, opCodeGTE, opGTE
	case "lt":
		return col, opCodeLT, opLT
	case "lte":
		return col, opCodeLTE, opLTE
	case "ieq":
		return col, opCodeIEq, opIEq
	case "match":
		return col, opCodeMatch, opMatch
	default:
		return s, opCodeEq, opEq
	}
}

func splitSliceColumnOperator(s, sep string) (string, opCode, string) {
	ind := strings.LastIndex(s, sep)
	if ind == -1 {
		return s, opCodeEq, opAny
	}

	col := s[:ind]
	op := s[ind+len(sep):]

	switch op {
	case "eq", "":
		return col, opCodeEq, opAny
	case "neq", "exclude":
		return col, opCodeNotEq, opAll
	default:
		return s, opCodeEq, opAny
	}
}
