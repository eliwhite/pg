package types

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"net"
	"reflect"
	"strconv"
	"sync"
	"time"

	"github.com/go-pg/pg/v9/internal"
	_struct "github.com/golang/protobuf/ptypes/struct"
	timestamp "github.com/golang/protobuf/ptypes/timestamp"
)

var driverValuerType = reflect.TypeOf((*driver.Valuer)(nil)).Elem()
var appenderType = reflect.TypeOf((*ValueAppender)(nil)).Elem()

type AppenderFunc func([]byte, reflect.Value, int) []byte

var appenders []AppenderFunc

//nolint
func init() {
	appenders = []AppenderFunc{
		reflect.Bool:          appendBoolValue,
		reflect.Int:           appendIntValue,
		reflect.Int8:          appendIntValue,
		reflect.Int16:         appendIntValue,
		reflect.Int32:         appendIntValue,
		reflect.Int64:         appendIntValue,
		reflect.Uint:          appendUintValue,
		reflect.Uint8:         appendUintValue,
		reflect.Uint16:        appendUintValue,
		reflect.Uint32:        appendUintValue,
		reflect.Uint64:        appendUintValue,
		reflect.Uintptr:       nil,
		reflect.Float32:       appendFloat32Value,
		reflect.Float64:       appendFloat64Value,
		reflect.Complex64:     nil,
		reflect.Complex128:    nil,
		reflect.Array:         appendJSONValue,
		reflect.Chan:          nil,
		reflect.Func:          nil,
		reflect.Interface:     appendIfaceValue,
		reflect.Map:           appendJSONValue,
		reflect.Ptr:           nil,
		reflect.Slice:         appendJSONValue,
		reflect.String:        appendStringValue,
		reflect.Struct:        appendStructValue,
		reflect.UnsafePointer: nil,
	}
}

var appendersMap sync.Map

// RegisterAppender registers an appender func for the type.
// Expecting to be used only during initialization, it panics
// if there is already a registered appender for the given type.
func RegisterAppender(value interface{}, fn AppenderFunc) {
	registerAppender(reflect.TypeOf(value), fn)
}

func registerAppender(typ reflect.Type, fn AppenderFunc) {
	_, loaded := appendersMap.LoadOrStore(typ, fn)
	if loaded {
		err := fmt.Errorf("pg: appender for the type=%s is already registered",
			typ.String())
		panic(err)
	}
}

func Appender(typ reflect.Type) AppenderFunc {
	if v, ok := appendersMap.Load(typ); ok {
		return v.(AppenderFunc)
	}
	fn := appender(typ, false)
	_, _ = appendersMap.LoadOrStore(typ, fn)
	return fn
}

func appender(typ reflect.Type, pgArray bool) AppenderFunc {
	switch typ {
	case timeType:
		return appendTimeValue
	case grpcTimeType:
		return appendGrpcTimeValue
	case grpcStructType:
		return appendGrpcStructValue
	case ipType:
		return appendIPValue
	case ipNetType:
		return appendIPNetValue
	case jsonRawMessageType:
		return appendJSONRawMessageValue
	}

	if typ.Implements(appenderType) {
		return appendAppenderValue
	}
	if typ.Implements(driverValuerType) {
		return appendDriverValuerValue
	}

	kind := typ.Kind()
	switch kind {
	case reflect.Ptr:
		return ptrAppenderFunc(typ)
	case reflect.Slice:
		if typ.Elem().Kind() == reflect.Uint8 {
			return appendBytesValue
		}
		if pgArray {
			return ArrayAppender(typ)
		}
	case reflect.Array:
		if typ.Elem().Kind() == reflect.Uint8 {
			return appendArrayBytesValue
		}
	}
	return appenders[kind]
}

func ptrAppenderFunc(typ reflect.Type) AppenderFunc {
	appender := Appender(typ.Elem())
	return func(b []byte, v reflect.Value, flags int) []byte {
		if v.IsNil() {
			return AppendNull(b, flags)
		}
		return appender(b, v.Elem(), flags)
	}
}

func appendValue(b []byte, v reflect.Value, flags int) []byte {
	if v.Kind() == reflect.Ptr && v.IsNil() {
		return AppendNull(b, flags)
	}
	appender := Appender(v.Type())
	return appender(b, v, flags)
}

func appendIfaceValue(b []byte, v reflect.Value, flags int) []byte {
	return Append(b, v.Interface(), flags)
}

func appendBoolValue(b []byte, v reflect.Value, _ int) []byte {
	return appendBool(b, v.Bool())
}

func appendIntValue(b []byte, v reflect.Value, _ int) []byte {
	return strconv.AppendInt(b, v.Int(), 10)
}

func appendUintValue(b []byte, v reflect.Value, _ int) []byte {
	return strconv.AppendUint(b, v.Uint(), 10)
}

func appendFloat32Value(b []byte, v reflect.Value, flags int) []byte {
	return appendFloat(b, v.Float(), flags, 32)
}

func appendFloat64Value(b []byte, v reflect.Value, flags int) []byte {
	return appendFloat(b, v.Float(), flags, 64)
}

func appendBytesValue(b []byte, v reflect.Value, flags int) []byte {
	return AppendBytes(b, v.Bytes(), flags)
}

func appendArrayBytesValue(b []byte, v reflect.Value, flags int) []byte {
	return AppendBytes(b, v.Slice(0, v.Len()).Bytes(), flags)
}

func appendStringValue(b []byte, v reflect.Value, flags int) []byte {
	return AppendString(b, v.String(), flags)
}

func appendStructValue(b []byte, v reflect.Value, flags int) []byte {
	if v.Type() == timeType {
		return appendTimeValue(b, v, flags)
	}
	return appendJSONValue(b, v, flags)
}

func appendJSONValue(b []byte, v reflect.Value, flags int) []byte {
	bytes, err := json.Marshal(v.Interface())
	if err != nil {
		return AppendError(b, err)
	}
	return AppendJSONB(b, bytes, flags)
}

func appendTimeValue(b []byte, v reflect.Value, flags int) []byte {
	tm := v.Interface().(time.Time)
	return AppendTime(b, tm, flags)
}

func appendGrpcTimeValue(b []byte, v reflect.Value, quote int) []byte {
	tm := v.Interface().(timestamp.Timestamp)
	return AppendGrpcTime(b, tm, quote)
}

func appendIPValue(b []byte, v reflect.Value, flags int) []byte {
	ip := v.Interface().(net.IP)
	return AppendString(b, ip.String(), flags)
}

func appendIPNetValue(b []byte, v reflect.Value, flags int) []byte {
	ipnet := v.Interface().(net.IPNet)
	return AppendString(b, ipnet.String(), flags)
}

func appendJSONRawMessageValue(b []byte, v reflect.Value, flags int) []byte {
	return AppendString(b, internal.BytesToString(v.Bytes()), flags)
}

func appendAppenderValue(b []byte, v reflect.Value, flags int) []byte {
	return appendAppender(b, v.Interface().(ValueAppender), flags)
}

func appendDriverValuerValue(b []byte, v reflect.Value, flags int) []byte {
	return appendDriverValuer(b, v.Interface().(driver.Valuer), flags)
}

func appendGrpcStructValue(b []byte, v reflect.Value, flags int) []byte {
	s := v.Interface().(_struct.Struct)
	m := DecodeToMap(&s)
	bytes, err := json.Marshal(m)
	if err != nil {
		return AppendError(b, err)
	}
	return AppendJSONB(b, bytes, flags)
}

// DecodeToMap converts a pb.Struct to a map from strings to Go types.
// DecodeToMap panics if s is invalid.
func DecodeToMap(s *_struct.Struct) map[string]interface{} {
	if s == nil {
		return nil
	}
	m := map[string]interface{}{}
	for k, v := range s.Fields {
		m[k] = decodeValue(v)
	}
	return m
}

func decodeValue(v *_struct.Value) interface{} {
	switch k := v.Kind.(type) {
	case *_struct.Value_NullValue:
		return nil
	case *_struct.Value_NumberValue:
		return k.NumberValue
	case *_struct.Value_StringValue:
		return k.StringValue
	case *_struct.Value_BoolValue:
		return k.BoolValue
	case *_struct.Value_StructValue:
		return DecodeToMap(k.StructValue)
	case *_struct.Value_ListValue:
		s := make([]interface{}, len(k.ListValue.Values))
		for i, e := range k.ListValue.Values {
			s[i] = decodeValue(e)
		}
		return s
	default:
		panic("protostruct: unknown kind")
	}
}
