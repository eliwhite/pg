package types

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"reflect"
	"sync"
	"time"

	"github.com/golang/protobuf/ptypes"
	_struct "github.com/golang/protobuf/ptypes/struct"
	timestamp "github.com/golang/protobuf/ptypes/timestamp"
	"github.com/valyala/fastjson"

	"github.com/go-pg/pg/v9/internal"
)

var valueScannerType = reflect.TypeOf((*ValueScanner)(nil)).Elem()
var sqlScannerType = reflect.TypeOf((*sql.Scanner)(nil)).Elem()
var timeType = reflect.TypeOf((*time.Time)(nil)).Elem()
var grpcTimeType = reflect.TypeOf((*timestamp.Timestamp)(nil)).Elem()
var ipType = reflect.TypeOf((*net.IP)(nil)).Elem()
var ipNetType = reflect.TypeOf((*net.IPNet)(nil)).Elem()
var jsonRawMessageType = reflect.TypeOf((*json.RawMessage)(nil)).Elem()
var grpcStructType = reflect.TypeOf((*_struct.Struct)(nil)).Elem()

type ScannerFunc func(reflect.Value, Reader, int) error

var valueScanners []ScannerFunc

//nolint
func init() {
	valueScanners = []ScannerFunc{
		reflect.Bool:          scanBoolValue,
		reflect.Int:           scanInt64Value,
		reflect.Int8:          scanInt64Value,
		reflect.Int16:         scanInt64Value,
		reflect.Int32:         scanInt64Value,
		reflect.Int64:         scanInt64Value,
		reflect.Uint:          scanUint64Value,
		reflect.Uint8:         scanUint64Value,
		reflect.Uint16:        scanUint64Value,
		reflect.Uint32:        scanUint64Value,
		reflect.Uint64:        scanUint64Value,
		reflect.Uintptr:       nil,
		reflect.Float32:       scanFloat32Value,
		reflect.Float64:       scanFloat64Value,
		reflect.Complex64:     nil,
		reflect.Complex128:    nil,
		reflect.Array:         scanJSONValue,
		reflect.Chan:          nil,
		reflect.Func:          nil,
		reflect.Interface:     scanIfaceValue,
		reflect.Map:           scanJSONValue,
		reflect.Ptr:           nil,
		reflect.Slice:         scanJSONValue,
		reflect.String:        scanStringValue,
		reflect.Struct:        scanJSONValue,
		reflect.UnsafePointer: nil,
	}
}

var scannersMap sync.Map

// RegisterScanner registers an scanner func for the type.
// Expecting to be used only during initialization, it panics
// if there is already a registered scanner for the given type.
func RegisterScanner(value interface{}, fn ScannerFunc) {
	registerScanner(reflect.TypeOf(value), fn)
}

func registerScanner(typ reflect.Type, fn ScannerFunc) {
	_, loaded := scannersMap.LoadOrStore(typ, fn)
	if loaded {
		err := fmt.Errorf("pg: scanner for the type=%s is already registered",
			typ.String())
		panic(err)
	}
}

func Scanner(typ reflect.Type) ScannerFunc {
	if v, ok := scannersMap.Load(typ); ok {
		return v.(ScannerFunc)
	}
	fn := scanner(typ, false)
	_, _ = scannersMap.LoadOrStore(typ, fn)
	return fn
}

func scanner(typ reflect.Type, pgArray bool) ScannerFunc {
	switch typ {
	case timeType:
		return scanTimeValue
	case grpcTimeType:
		return scanGrpcTimeValue
	case grpcStructType:
		return scanGrpcStructValue
	case ipType:
		return scanIPValue
	case ipNetType:
		return scanIPNetValue
	case jsonRawMessageType:
		return scanJSONRawMessageValue
	}

	if typ.Implements(valueScannerType) {
		return scanValueScannerValue
	}
	if reflect.PtrTo(typ).Implements(valueScannerType) {
		return scanValueScannerAddrValue
	}

	if typ.Implements(sqlScannerType) {
		return scanSQLScannerValue
	}
	if reflect.PtrTo(typ).Implements(sqlScannerType) {
		return scanSQLScannerAddrValue
	}

	kind := typ.Kind()
	switch kind {
	case reflect.Ptr:
		return ptrScannerFunc(typ)
	case reflect.Slice:
		if typ.Elem().Kind() == reflect.Uint8 {
			return scanBytesValue
		}
		if pgArray {
			return ArrayScanner(typ)
		}
	case reflect.Array:
		if typ.Elem().Kind() == reflect.Uint8 {
			return scanArrayBytesValue
		}
	}
	return valueScanners[kind]
}

func ptrScannerFunc(typ reflect.Type) ScannerFunc {
	scanner := Scanner(typ.Elem())
	return func(v reflect.Value, rd Reader, n int) error {
		if scanner == nil {
			return fmt.Errorf("pg: Scan(unsupported %s)", v.Type())
		}

		if n == -1 {
			if v.IsNil() {
				return nil
			}
			if !v.CanSet() {
				return fmt.Errorf("pg: Scan(nonsettable %s)", v.Type())
			}
			v.Set(reflect.Zero(v.Type()))
			return nil
		}

		if v.IsNil() {
			if !v.CanSet() {
				return fmt.Errorf("pg: Scan(nonsettable %s)", v.Type())
			}
			v.Set(reflect.New(v.Type().Elem()))
		}

		return scanner(v.Elem(), rd, n)
	}
}

func scanIfaceValue(v reflect.Value, rd Reader, n int) error {
	if v.IsNil() {
		return scanJSONValue(v, rd, n)
	}
	return ScanValue(v.Elem(), rd, n)
}

func ScanValue(v reflect.Value, rd Reader, n int) error {
	if !v.IsValid() {
		return errors.New("pg: Scan(nil)")
	}

	scanner := Scanner(v.Type())
	if scanner != nil {
		return scanner(v, rd, n)
	}

	if v.Kind() == reflect.Interface {
		return errors.New("pg: Scan(nil)")
	}
	return fmt.Errorf("pg: Scan(unsupported %s)", v.Type())
}

func scanBoolValue(v reflect.Value, rd Reader, n int) error {
	if !v.CanSet() {
		return fmt.Errorf("pg: Scan(nonsettable %s)", v.Type())
	}

	if n == -1 {
		v.SetBool(false)
		return nil
	}

	tmp, err := rd.ReadFullTemp()
	if err != nil {
		return err
	}

	v.SetBool(len(tmp) == 1 && (tmp[0] == 't' || tmp[0] == '1'))
	return nil
}

func scanInt64Value(v reflect.Value, rd Reader, n int) error {
	if !v.CanSet() {
		return fmt.Errorf("pg: Scan(nonsettable %s)", v.Type())
	}

	num, err := ScanInt64(rd, n)
	if err != nil {
		return err
	}

	v.SetInt(num)
	return nil
}

func scanUint64Value(v reflect.Value, rd Reader, n int) error {
	if !v.CanSet() {
		return fmt.Errorf("pg: Scan(nonsettable %s)", v.Type())
	}

	num, err := ScanUint64(rd, n)
	if err != nil {
		return err
	}

	v.SetUint(num)
	return nil
}

func scanFloat32Value(v reflect.Value, rd Reader, n int) error {
	if !v.CanSet() {
		return fmt.Errorf("pg: Scan(nonsettable %s)", v.Type())
	}

	num, err := ScanFloat32(rd, n)
	if err != nil {
		return err
	}

	v.SetFloat(float64(num))
	return nil
}

func scanFloat64Value(v reflect.Value, rd Reader, n int) error {
	if !v.CanSet() {
		return fmt.Errorf("pg: Scan(nonsettable %s)", v.Type())
	}

	num, err := ScanFloat64(rd, n)
	if err != nil {
		return err
	}

	v.SetFloat(num)
	return nil
}

func scanStringValue(v reflect.Value, rd Reader, n int) error {
	if !v.CanSet() {
		return fmt.Errorf("pg: Scan(nonsettable %s)", v.Type())
	}

	s, err := ScanString(rd, n)
	if err != nil {
		return err
	}

	v.SetString(s)
	return nil
}

func scanJSONValue(v reflect.Value, rd Reader, n int) error {
	if !v.CanSet() {
		return fmt.Errorf("pg: Scan(nonsettable %s)", v.Type())
	}

	// Zero value so it works with SelectOrInsert.
	// TODO: better handle slices
	v.Set(reflect.New(v.Type()).Elem())

	if n == -1 {
		return nil
	}

	dec := json.NewDecoder(rd)
	return dec.Decode(v.Addr().Interface())
}

func scanTimeValue(v reflect.Value, rd Reader, n int) error {
	if !v.CanSet() {
		return fmt.Errorf("pg: Scan(nonsettable %s)", v.Type())
	}

	tm, err := ScanTime(rd, n)
	if err != nil {
		return err
	}

	v.Set(reflect.ValueOf(tm))
	return nil
}

//var zeroGrpcTimeValue = reflect.ValueOf(types.Timestamp{})

func scanGrpcTimeValue(v reflect.Value, rd Reader, n int) error {

	//func scanGrpcTimeValue(v reflect.Value, b []byte) error {
	if !v.CanSet() {
		return fmt.Errorf("pg: Scan(nonsettable %s)", v.Type())
	}
	if n == -1 {
		v.Set(reflect.ValueOf(timestamp.Timestamp{}))
		//v.Set(zeroGrpcTimeValue)
		return nil
	}
	tm, err := ScanTime(rd, n)
	if err != nil {
		return err
	}
	ts, err := ptypes.TimestampProto(tm)
	if err != nil {
		return err
	}
	v.Set(reflect.ValueOf(*ts))
	return nil
}

func scanIPValue(v reflect.Value, rd Reader, n int) error {
	if !v.CanSet() {
		return fmt.Errorf("pg: Scan(nonsettable %s)", v.Type())
	}

	if n == -1 {
		return nil
	}

	tmp, err := rd.ReadFullTemp()
	if err != nil {
		return err
	}

	ip := net.ParseIP(internal.BytesToString(tmp))
	if ip == nil {
		return fmt.Errorf("pg: invalid ip=%q", tmp)
	}

	v.Set(reflect.ValueOf(ip))
	return nil
}

var zeroIPNetValue = reflect.ValueOf(net.IPNet{})

func scanIPNetValue(v reflect.Value, rd Reader, n int) error {
	if !v.CanSet() {
		return fmt.Errorf("pg: Scan(nonsettable %s)", v.Type())
	}

	if n == -1 {
		v.Set(zeroIPNetValue)
		return nil
	}

	tmp, err := rd.ReadFullTemp()
	if err != nil {
		return err
	}

	_, ipnet, err := net.ParseCIDR(internal.BytesToString(tmp))
	if err != nil {
		return err
	}

	v.Set(reflect.ValueOf(*ipnet))
	return nil
}

func scanJSONRawMessageValue(v reflect.Value, rd Reader, n int) error {
	if !v.CanSet() {
		return fmt.Errorf("pg: Scan(nonsettable %s)", v.Type())
	}

	if n == -1 {
		v.SetBytes(nil)
		return nil
	}

	b, err := rd.ReadFull()
	if err != nil {
		return err
	}

	v.SetBytes(b)
	return nil
}

func scanBytesValue(v reflect.Value, rd Reader, n int) error {
	if !v.CanSet() {
		return fmt.Errorf("pg: Scan(nonsettable %s)", v.Type())
	}

	if n == -1 {
		v.SetBytes(nil)
		return nil
	}

	b, err := ScanBytes(rd, n)
	if err != nil {
		return err
	}

	v.SetBytes(b)
	return nil
}

func scanArrayBytesValue(v reflect.Value, rd Reader, n int) error {
	b := v.Slice(0, v.Len()).Bytes()

	if n == -1 {
		for i := range b {
			b[i] = 0
		}
		return nil
	}

	_, err := readBytes(rd, b)
	return err
}

func scanValueScannerValue(v reflect.Value, rd Reader, n int) error {
	if n == -1 {
		if v.IsNil() {
			return nil
		}
		return v.Interface().(ValueScanner).ScanValue(rd, n)
	}

	if v.IsNil() {
		v.Set(reflect.New(v.Type().Elem()))
	}

	return v.Interface().(ValueScanner).ScanValue(rd, n)
}

func scanValueScannerAddrValue(v reflect.Value, rd Reader, n int) error {
	if !v.CanAddr() {
		return fmt.Errorf("pg: Scan(nonsettable %s)", v.Type())
	}
	return v.Addr().Interface().(ValueScanner).ScanValue(rd, n)
}

func scanSQLScannerValue(v reflect.Value, rd Reader, n int) error {
	if n == -1 {
		if v.IsNil() {
			return nil
		}
		return scanSQLScanner(v.Interface().(sql.Scanner), rd, n)
	}

	if v.IsNil() {
		v.Set(reflect.New(v.Type().Elem()))
	}

	return scanSQLScanner(v.Interface().(sql.Scanner), rd, n)
}

func scanSQLScannerAddrValue(v reflect.Value, rd Reader, n int) error {
	if !v.CanAddr() {
		return fmt.Errorf("pg: Scan(nonsettable %s)", v.Type())
	}
	return scanSQLScanner(v.Addr().Interface().(sql.Scanner), rd, n)
}

func scanSQLScanner(scanner sql.Scanner, rd Reader, n int) error {
	if n == -1 {
		return scanner.Scan(nil)
	}

	tmp, err := rd.ReadFullTemp()
	if err != nil {
		return err
	}
	return scanner.Scan(tmp)
}

func scanGrpcStructValue(val reflect.Value, rd Reader, n int) error {
	if !val.CanSet() {
		return fmt.Errorf("pg: Scan(nonsettable %s)", val.Type())
	}

	if n == -1 {
		return nil
	}

	b, err := rd.ReadFull()
	if err != nil {
		return err
	}
	var p fastjson.Parser
	v, err := p.Parse(string(b))
	if err != nil {
		return err
	}
	fields := make(map[string]*_struct.Value)

	v.GetObject().Visit(func(k []byte, v *fastjson.Value) {
		newv := decodeJSONToStructValue(v)
		fields[string(k)] = &newv
	})
	val.Set(reflect.ValueOf(_struct.Struct{Fields: fields}))
	return nil
}

//func decodeJSONToStructValue(k []byte, v *fastjson.Value) _struct.Value {
func decodeJSONToStructValue(v *fastjson.Value) _struct.Value {
	switch v.Type() {
	case fastjson.TypeNumber:
		return _struct.Value{Kind: &_struct.Value_NumberValue{NumberValue: v.GetFloat64()}}
	case fastjson.TypeString:
		return _struct.Value{Kind: &_struct.Value_StringValue{StringValue: v.String()}}
	case fastjson.TypeFalse:
		return _struct.Value{Kind: &_struct.Value_BoolValue{BoolValue: false}}
	case fastjson.TypeTrue:
		return _struct.Value{Kind: &_struct.Value_BoolValue{BoolValue: true}}
	case fastjson.TypeObject:
		fields := make(map[string]*_struct.Value)
		v.GetObject().Visit(func(k []byte, v2 *fastjson.Value) {
			newv := decodeJSONToStructValue(v2)
			fields[string(k)] = &newv
		})
		return _struct.Value{Kind: &_struct.Value_StructValue{StructValue: &_struct.Struct{Fields: fields}}}
		/*
			case map[string]interface{}:
				vals := _struct.Struct{Fields: make(map[string]*_struct.Value)}
				for i, v := range k {
					newVal := decodeToStructValue(v)
					vals.Fields[i] = &newVal
				}
				return _struct.Value{Kind: &_struct.Value_StructValue{StructValue: &vals}}

			case map[interface{}]interface{}:
				vals := _struct.Struct{Fields: make(map[string]*_struct.Value)}
				for i, v := range k {
					newVal := decodeToStructValue(v)
					vals.Fields[i.(string)] = &newVal
				}
				return _struct.Value{Kind: &_struct.Value_StructValue{StructValue: &vals}}
			case []interface{}:
				vals := _struct.ListValue{}
				for _, v := range k {
					newVal := decodeToStructValue(v)
					vals.Values = append(vals.Values, &newVal)
				}
				return _struct.Value{Kind: &_struct.Value_ListValue{ListValue: &vals}}
		*/
	default:
		return _struct.Value{}
	}
}
