package validator

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	"github.com/go-playground/validator/v10"
)

var validate *validator.Validate

// slugRe allows lowercase letters, numbers, and hyphens (no leading/trailing hyphens)
var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9\-]*[a-z0-9]$|^[a-z0-9]$`)

// envKeyRe: uppercase letters, digits, underscores; must start with letter or underscore
var envKeyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func init() {
	validate = validator.New()

	// Custom "slug" tag
	_ = validate.RegisterValidation("slug", func(fl validator.FieldLevel) bool {
		return slugRe.MatchString(fl.Field().String())
	})

	// Custom "envkey" tag
	_ = validate.RegisterValidation("envkey", func(fl validator.FieldLevel) bool {
		return envKeyRe.MatchString(fl.Field().String())
	})
}

// DecodeAndValidate decodes JSON body into v and validates it.
// On failure it writes appropriate JSON error and returns false.
func DecodeAndValidate(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1 MB max body
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON: "+sanitizeErrMsg(err.Error()))
		return false
	}

	if err := validate.Struct(v); err != nil {
		msgs := make([]string, 0)
		for _, fe := range err.(validator.ValidationErrors) {
			msgs = append(msgs, fieldErrMsg(fe))
		}
		writeErr(w, http.StatusUnprocessableEntity, strings.Join(msgs, "; "))
		return false
	}
	return true
}

func fieldErrMsg(fe validator.FieldError) string {
	switch fe.Tag() {
	case "required":
		return fe.Field() + " is required"
	case "email":
		return fe.Field() + " must be a valid email"
	case "min":
		return fe.Field() + " is too short (min " + fe.Param() + ")"
	case "max":
		return fe.Field() + " is too long (max " + fe.Param() + ")"
	case "oneof":
		return fe.Field() + " must be one of: " + fe.Param()
	case "slug":
		return fe.Field() + " must be lowercase alphanumeric with hyphens"
	case "envkey":
		return fe.Field() + " must match [A-Za-z_][A-Za-z0-9_]*"
	case "url":
		return fe.Field() + " must be a valid URL"
	case "fqdn":
		return fe.Field() + " must be a valid domain name"
	case "hexadecimal":
		return fe.Field() + " must be a hexadecimal value"
	default:
		return fe.Field() + " failed " + fe.Tag() + " validation"
	}
}

// sanitizeErrMsg strips potentially sensitive content from error messages
func sanitizeErrMsg(msg string) string {
	// Only keep safe printable ASCII, truncate
	var b strings.Builder
	for _, r := range msg {
		if r >= 32 && r < 127 {
			b.WriteRune(r)
		}
		if b.Len() > 200 {
			break
		}
	}
	return b.String()
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	enc, _ := json.Marshal(map[string]string{"error": msg})
	w.Write(enc)
}
