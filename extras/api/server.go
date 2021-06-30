package api

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"

	"github.com/anoop-dhiman/lbry.go/v2/extras/errors"
	"github.com/anoop-dhiman/lbry.go/v2/extras/util"
	"github.com/anoop-dhiman/lbry.go/v2/extras/validator"
	v "github.com/lbryio/ozzo-validation"

	"github.com/spf13/cast"
)

// ResponseHeaders are returned with each response
var ResponseHeaders map[string]string

// CorsDomains Allowed domains for CORS Policy
var CorsDomains []string

// CorsAllowLocalhost if true localhost connections are always allowed
var CorsAllowLocalhost bool

// Log allows logging of events and errors
var Log = func(*http.Request, *Response, error) {}

// http://choly.ca/post/go-json-marshalling/
type ResponseInfo struct {
	Success bool        `json:"success"`
	Error   *string     `json:"error"`
	Data    interface{} `json:"data"`
	Trace   []string    `json:"_trace,omitempty"`
}

// BuildJSONResponse allows implementers to control the json response form from the api
var BuildJSONResponse = func(response ResponseInfo) ([]byte, error) {
	return json.MarshalIndent(&response, "", "  ")
}

// TraceEnabled Attaches a trace field to the JSON response when enabled.
var TraceEnabled = false

// StatusError represents an error with an associated HTTP status code.
type StatusError struct {
	Status int
	Err    error
}

func (se StatusError) Error() string { return se.Err.Error() }

// Response is returned by API handlers
type Response struct {
	Status      int
	Data        interface{}
	RedirectURL string
	Error       error
}

// Handler handles API requests
type Handler func(r *http.Request) Response

func (h Handler) callHandlerSafely(r *http.Request) (rsp Response) {
	defer func() {
		if r := recover(); r != nil {
			err, ok := r.(error)
			if !ok {
				err = errors.Err("%v", r)
			}
			rsp = Response{Error: errors.Wrap(err, 2)}
		}
	}()

	return h(r)
}

func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Set header settings
	if ResponseHeaders != nil {
		//Multiple readers, no writers is okay
		for key, value := range ResponseHeaders {
			w.Header().Set(key, value)
		}
	}
	origin := r.Header.Get("origin")
	for _, d := range CorsDomains {
		if d == origin {
			w.Header().Set("Access-Control-Allow-Origin", d)
			vary := w.Header().Get("Vary")
			if vary != "*" {
				if vary != "" {
					vary += ", "
				}
				vary += "Origin"
			}
			w.Header().Set("Vary", vary)
		}
	}

	if CorsAllowLocalhost && strings.HasPrefix(origin, "http://localhost:") {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		vary := w.Header().Get("Vary")
		if vary != "*" {
			if vary != "" {
				vary += ", "
			}
			vary += "Origin"
		}
		w.Header().Set("Vary", vary)
	}

	// Stop here if its a preflighted OPTIONS request
	if r.Method == "OPTIONS" {
		return
	}

	rsp := h.callHandlerSafely(r)

	if rsp.Status == 0 {
		if rsp.Error != nil {
			ogErr := errors.Unwrap(rsp.Error)
			if statusError, ok := ogErr.(StatusError); ok {
				rsp.Status = statusError.Status
			} else {
				rsp.Status = http.StatusInternalServerError
			}
		} else if rsp.RedirectURL != "" {
			rsp.Status = http.StatusFound
		} else {
			rsp.Status = http.StatusOK
		}
	}

	success := rsp.Status < http.StatusBadRequest
	if success {
		Log(r, &rsp, nil)
	} else {
		Log(r, &rsp, rsp.Error)
	}

	// redirect
	if rsp.Status >= http.StatusMultipleChoices && rsp.Status < http.StatusBadRequest {
		http.Redirect(w, r, rsp.RedirectURL, rsp.Status)
		return
	} else if rsp.RedirectURL != "" {
		Log(r, &rsp, errors.Base(
			"status code %d does not indicate a redirect, but RedirectURL is non-empty '%s'",
			rsp.Status, rsp.RedirectURL,
		))
	}

	var errorString *string
	if rsp.Error != nil {
		errorStringRaw := rsp.Error.Error()
		errorString = &errorStringRaw
	}

	var trace []string
	if TraceEnabled && errors.HasTrace(rsp.Error) {
		trace = getTraceFromError(rsp.Error)
	}

	jsonResponse, err := BuildJSONResponse(ResponseInfo{
		Success: success,
		Error:   errorString,
		Data:    rsp.Data,
		Trace:   trace,
	})
	if err != nil {
		Log(r, &rsp, errors.Prefix("Error encoding JSON response: ", err))
		jsonResponse, err = BuildJSONResponse(ResponseInfo{
			Success: false,
			Error:   util.PtrToString(err.Error()),
			Data:    nil,
			Trace:   getTraceFromError(err),
		})
		if err != nil {
			Log(r, &rsp, errors.Prefix("Error encoding JSON response: ", err))
		}
	}

	w.WriteHeader(rsp.Status)
	_, _ = w.Write(jsonResponse)
}

func getTraceFromError(err error) []string {
	trace := strings.Split(errors.Trace(err), "\n")
	for index, element := range trace {
		if strings.HasPrefix(element, "\t") {
			trace[index] = strings.Replace(element, "\t", "    ", 1)
		}
	}
	return trace
}

// IgnoredFormFields are ignored by FormValues() when checking for extraneous fields
var IgnoredFormFields []string

func FormValues(r *http.Request, params interface{}, validationRules []*v.FieldRules) error {
	ref := reflect.ValueOf(params)
	if !ref.IsValid() || ref.Kind() != reflect.Ptr || ref.Elem().Kind() != reflect.Struct {
		return errors.Err("'params' must be a pointer to a struct")
	}

	structType := ref.Elem().Type()
	structValue := ref.Elem()
	fields := map[string]bool{}
	for i := 0; i < structType.NumField(); i++ {
		fieldName := structType.Field(i).Name
		formattedName := util.Underscore(fieldName)
		jsonName, ok := structType.Field(i).Tag.Lookup("json")
		if ok {
			formattedName = jsonName
		}
		value := strings.TrimSpace(r.FormValue(formattedName))

		// if param is not set at all, continue
		// comes after call to r.FormValue so form values get parsed internally (if they arent already)
		if len(r.Form[formattedName]) == 0 {
			continue
		}

		fields[formattedName] = true
		isPtr := false
		var finalValue reflect.Value

		structField := structValue.FieldByName(fieldName)
		structFieldKind := structField.Kind()
		if structFieldKind == reflect.Ptr {
			isPtr = true
			structFieldKind = structField.Type().Elem().Kind()
		}

		switch structFieldKind {
		case reflect.String:
			finalValue = reflect.ValueOf(value)
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			if value == "" {
				continue
			}
			castVal, err := cast.ToInt64E(value)
			if err != nil {
				return errors.Err("%s: must be an integer", formattedName)
			}
			switch structFieldKind {
			case reflect.Int:
				finalValue = reflect.ValueOf(int(castVal))
			case reflect.Int8:
				finalValue = reflect.ValueOf(int8(castVal))
			case reflect.Int16:
				finalValue = reflect.ValueOf(int16(castVal))
			case reflect.Int32:
				finalValue = reflect.ValueOf(int32(castVal))
			case reflect.Int64:
				finalValue = reflect.ValueOf(castVal)
			}
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			if value == "" {
				continue
			}
			castVal, err := cast.ToUint64E(value)
			if err != nil {
				return errors.Err("%s: must be an unsigned integer", formattedName)
			}
			switch structFieldKind {
			case reflect.Uint:
				finalValue = reflect.ValueOf(uint(castVal))
			case reflect.Uint8:
				finalValue = reflect.ValueOf(uint8(castVal))
			case reflect.Uint16:
				finalValue = reflect.ValueOf(uint16(castVal))
			case reflect.Uint32:
				finalValue = reflect.ValueOf(uint32(castVal))
			case reflect.Uint64:
				finalValue = reflect.ValueOf(castVal)
			}
		case reflect.Bool:
			if value == "" {
				continue
			}
			if !validator.IsBoolString(value) {
				return errors.Err("%s: must be one of the following values: %s",
					formattedName, strings.Join(validator.GetBoolStringValues(), ", "))
			}
			finalValue = reflect.ValueOf(validator.IsTruthy(value))

		case reflect.Float32, reflect.Float64:
			if value == "" {
				continue
			}
			castVal, err := cast.ToFloat64E(value)
			if err != nil {
				return errors.Err("%s: must be a floating point number", formattedName)
			}
			switch structFieldKind {
			case reflect.Float32:
				finalValue = reflect.ValueOf(float32(castVal))
			case reflect.Float64:
				finalValue = reflect.ValueOf(float64(castVal))
			}
		default:
			return errors.Err("field %s is an unsupported type", fieldName)
		}

		if isPtr {
			if structField.IsNil() {
				structField.Set(reflect.New(structField.Type().Elem()))
			}
			structField.Elem().Set(finalValue)
		} else {
			structField.Set(finalValue)
		}
	}

	var extraParams []string
	for k := range r.Form {
		if _, ok := fields[k]; !ok && !util.InSlice(k, IgnoredFormFields) {
			extraParams = append(extraParams, k)
		}
	}
	if len(extraParams) > 0 {
		return errors.Err("Extraneous params: " + strings.Join(extraParams, ", "))
	}

	if len(validationRules) > 0 {
		validationErr := v.ValidateStruct(params, validationRules...)
		if validationErr != nil {
			return errors.Err(validationErr)
		}
	}

	return nil
}
