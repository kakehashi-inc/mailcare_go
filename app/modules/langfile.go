package modules

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"strings"

	"mailcare/app/models"
)

// LanguageFile is a language file of the Web UI
// (frontend/src/i18n/<language>.json in FrontendFS) read on the server,
// for what the server words for a user itself: the category names of the
// notification mail and every word of the PDF report of a group. The keys
// are the same full keys the Web UI uses (system design document 10.1).
type LanguageFile struct {
	Language string // the language of the file actually read
	root     map[string]any
}

// placeholderRe matches an i18next placeholder ("{{name}}").
var placeholderRe = regexp.MustCompile(`\{\{\s*([A-Za-z0-9_]+)\s*\}\}`)

// LoadLanguageFile reads the language file of a language, or the Japanese
// one when there is no file for the language.
func LoadLanguageFile(language string) (*LanguageFile, error) {
	if FrontendFS == nil {
		return nil, errors.New("language files are not available")
	}
	read := language
	data, err := fs.ReadFile(FrontendFS, "frontend/src/i18n/"+language+".json")
	if errors.Is(err, fs.ErrNotExist) && language != models.DefaultLanguage {
		read = models.DefaultLanguage
		data, err = fs.ReadFile(FrontendFS, "frontend/src/i18n/"+read+".json")
	}
	if err != nil {
		return nil, fmt.Errorf("language file: %w", err)
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("language file %s: %w", read, err)
	}
	return &LanguageFile{Language: read, root: root}, nil
}

// Lookup returns the text of a full key ("field.group.title") with its
// placeholders replaced by params (a placeholder without a value stays as
// it is); ok is false when the file has no text under the key.
func (f *LanguageFile) Lookup(key string, params map[string]any) (text string, ok bool) {
	var node any = f.root
	for part := range strings.SplitSeq(key, ".") {
		m, isMap := node.(map[string]any)
		if !isMap {
			return "", false
		}
		if node, ok = m[part]; !ok {
			return "", false
		}
	}
	text, ok = node.(string)
	if !ok {
		return "", false
	}
	return placeholderRe.ReplaceAllStringFunc(text, func(p string) string {
		name := placeholderRe.FindStringSubmatch(p)[1]
		if v, found := params[name]; found {
			return fmt.Sprint(v)
		}
		return p
	}), true
}

// T is Lookup that answers the key itself when the file has no text for it.
func (f *LanguageFile) T(key string, params map[string]any) string {
	if text, ok := f.Lookup(key, params); ok {
		return text
	}
	return key
}

// categoryLabels returns the category names of the file
// (value.category.<category>.label).
func (f *LanguageFile) categoryLabels() map[string]string {
	labels := map[string]string{}
	value, _ := f.root["value"].(map[string]any)
	categories, _ := value["category"].(map[string]any)
	for key, c := range categories {
		if entry, ok := c.(map[string]any); ok {
			if label, ok := entry["label"].(string); ok {
				labels[key] = label
			}
		}
	}
	return labels
}
