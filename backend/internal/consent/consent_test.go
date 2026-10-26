package consent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/brf-tech/filex-apps/backend/internal/domain"
	"github.com/brf-tech/filex-apps/backend/internal/filetypes"
)

func TestParse_AFullFile(t *testing.T) {
	c, err := Parse([]byte(`{
  "publisher": "brf-tech",
  "categories": ["documents", "security"],
  "summary": {"en": "Sign PDFs in filex.", "tr": "filex'te PDF imzalayın."},
  "description": "docs/STORE.md",
  "icon": "assets/icon-256.png",
  "screenshots": [
    {"path": "assets/shot-1.png", "caption": {"en": "The signing page"}},
    {"path": "assets/shot-2.webp"}
  ],
  "conversions": [
    {"from": ["docx", "odt"], "to": ["pdf", "tar.gz"]},
    {"from": ["doc"], "to": ["pdf"], "needs": ["office"]}
  ]
}`))
	if err != nil {
		t.Fatal(err)
	}
	want := &domain.Consent{
		Publisher:   "brf-tech",
		Categories:  []string{"documents", "security"},
		Summary:     domain.Text{"en": "Sign PDFs in filex.", "tr": "filex'te PDF imzalayın."},
		Description: "docs/STORE.md",
		Icon:        "assets/icon-256.png",
		Screenshots: []domain.ConsentShot{
			{Path: "assets/shot-1.png", Caption: domain.Text{"en": "The signing page"}},
			{Path: "assets/shot-2.webp"},
		},
		Conversions: []filetypes.Conversion{
			{From: []string{"docx", "odt"}, To: []string{"pdf", "tar.gz"}},
			{From: []string{"doc"}, To: []string{"pdf"}, Needs: []string{"office"}},
		},
	}
	if !reflect.DeepEqual(c, want) {
		t.Errorf("got %+v", c)
	}
	if c, err := Parse([]byte(`{"publisher":"abc"}`)); err != nil || c.Publisher != "abc" {
		t.Errorf("the smallest file: %v", err)
	}
}

func TestParse_RefusesWhatTheSchemaRefuses(t *testing.T) {
	long := strings.Repeat("a", MaxTextLen+1)
	for name, c := range map[string]struct {
		doc  string
		want string
	}{
		"not an object":         {`["publisher"]`, "not a JSON object"},
		"not JSON":              {`{"publisher": "x",}`, "not a JSON object"},
		"trailing data":         {`{"publisher":"abc"} {}`, "data after the object"},
		"duplicate key":         {`{"publisher":"abc","publisher":"evil"}`, "appears twice"},
		"no publisher":          {`{"summary":{"en":"x"}}`, "publisher: required"},
		"publisher pattern":     {`{"publisher":"BRF"}`, "publisher:"},
		"publisher too short":   {`{"publisher":"ab"}`, "publisher:"},
		"publisher ends dash":   {`{"publisher":"abc-"}`, "publisher:"},
		"publisher null":        {`{"publisher":null}`, "publisher: must be a string"},
		"unknown field":         {`{"publisher":"abc","homepage":"x"}`, "homepage: unknown field"},
		"four categories":       {`{"publisher":"abc","categories":["a","b","c","d"]}`, "at most 3"},
		"category pattern":      {`{"publisher":"abc","categories":["Docs"]}`, "categories[0]"},
		"category twice":        {`{"publisher":"abc","categories":["a","a"]}`, "appears twice"},
		"categories not array":  {`{"publisher":"abc","categories":"a"}`, "categories: must be an array"},
		"summary without en":    {`{"publisher":"abc","summary":{"tr":"x"}}`, `"en" is required`},
		"summary bad language":  {`{"publisher":"abc","summary":{"en":"x","EN":"y"}}`, "not a language tag"},
		"summary empty text":    {`{"publisher":"abc","summary":{"en":""}}`, "summary.en: 1 to"},
		"summary too long":      {`{"publisher":"abc","summary":{"en":"` + long + `"}}`, "summary.en: 1 to"},
		"summary not text":      {`{"publisher":"abc","summary":{"en":3}}`, "summary.en: must be a string"},
		"absolute icon":         {`{"publisher":"abc","icon":"/etc/passwd"}`, "icon:"},
		"dotdot icon":           {`{"publisher":"abc","icon":"assets/../../x.png"}`, "icon:"},
		"dotdot description":    {`{"publisher":"abc","description":".."}`, "description:"},
		"icon odd characters":   {`{"publisher":"abc","icon":"assets/icon 1.png"}`, "icon:"},
		"icon url":              {`{"publisher":"abc","icon":"https://evil.example/x.png"}`, "icon:"},
		"empty path":            {`{"publisher":"abc","icon":""}`, "icon:"},
		"nine screenshots":      {`{"publisher":"abc","screenshots":[` + strings.TrimSuffix(strings.Repeat(`{"path":"a.png"},`, 9), ",") + `]}`, "at most 8"},
		"screenshot no path":    {`{"publisher":"abc","screenshots":[{"caption":{"en":"x"}}]}`, "screenshots[0].path: required"},
		"screenshot extra":      {`{"publisher":"abc","screenshots":[{"path":"a.png","alt":"x"}]}`, "screenshots[0].alt: unknown field"},
		"screenshot not object": {`{"publisher":"abc","screenshots":["a.png"]}`, "screenshots[0]: must be an object"},
		"not UTF-8":             {"{\"publisher\":\"abc\",\"summary\":{\"en\":\"\xff\"}}", "not UTF-8"},
		"conversions empty":     {`{"publisher":"abc","conversions":[]}`, "conversions: at least 1 entry"},
		"conversions not array": {`{"publisher":"abc","conversions":{"from":["a"]}}`, "conversions: must be an array"},
		"conversion no to":      {`{"publisher":"abc","conversions":[{"from":["docx"]}]}`, "conversions[0].to: required"},
		"conversion no from":    {`{"publisher":"abc","conversions":[{"to":["pdf"]}]}`, "conversions[0].from: required"},
		"conversion empty from": {`{"publisher":"abc","conversions":[{"from":[],"to":["pdf"]}]}`, "conversions[0].from: 1 to"},
		"conversion dotted ext": {`{"publisher":"abc","conversions":[{"from":[".docx"],"to":["pdf"]}]}`, "conversions[0].from[0]"},
		"conversion upper ext":  {`{"publisher":"abc","conversions":[{"from":["DOCX"],"to":["pdf"]}]}`, "conversions[0].from[0]"},
		"conversion glob ext":   {`{"publisher":"abc","conversions":[{"from":["*"],"to":["pdf"]}]}`, "conversions[0].from[0]"},
		"conversion ext twice":  {`{"publisher":"abc","conversions":[{"from":["docx","docx"],"to":["pdf"]}]}`, `"docx" appears twice`},
		"conversion to itself":  {`{"publisher":"abc","conversions":[{"from":["pdf","docx"],"to":["pdf"]}]}`, `"pdf" is in both from and to`},
		"conversion extra":      {`{"publisher":"abc","conversions":[{"from":["a"],"to":["b"],"via":"x"}]}`, "conversions[0].via: unknown field"},
		"conversion needs none": {`{"publisher":"abc","conversions":[{"from":["a"],"to":["b"],"needs":[]}]}`, "conversions[0].needs: 1 to"},
		"conversion bad engine": {`{"publisher":"abc","conversions":[{"from":["a"],"to":["b"],"needs":["Office"]}]}`, "conversions[0].needs[0]"},
		"conversion not object": {`{"publisher":"abc","conversions":["docx"]}`, "conversions[0]: must be an object"},
		"too many conversions":  {`{"publisher":"abc","conversions":[` + strings.TrimSuffix(strings.Repeat(`{"from":["a"],"to":["b"]},`, MaxConversions+1), ",") + `]}`, "at most 256 entries"},
		"too many pairs":        {`{"publisher":"abc","conversions":[` + strings.TrimSuffix(strings.Repeat(manyPairs, 2), ",") + `]}`, "from-to pairs, at most"},
	} {
		_, err := Parse([]byte(c.doc))
		var ce *Error
		if !errors.As(err, &ce) {
			t.Errorf("%s: got %v, want a *consent.Error", name, err)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %q does not say %q", name, err.Error(), c.want)
		}
	}
	if _, err := Parse([]byte(`{"publisher":"abc","icon":"` + strings.Repeat("a", MaxPathLen) + `"}`)); err != nil {
		t.Errorf("a path of exactly %d characters: %v", MaxPathLen, err)
	}
	if _, err := Parse(append([]byte(`{"publisher":"abc","x":"`), make([]byte, MaxBytes)...)); err == nil {
		t.Error("a file over the cap must be refused")
	}
}

// manyPairs is one entry of 128 x 128 = 16384 pairs, followed by a comma:
// one fits the cap exactly, two exceed it.
var manyPairs = func() string {
	exts := func(p string) string {
		var b strings.Builder
		for i := 0; i < MaxConversionExts; i++ {
			if i > 0 {
				b.WriteString(",")
			}
			b.WriteString(`"` + p + string(rune('a'+i%26)) + string(rune('a'+i/26)) + `"`)
		}
		return "[" + b.String() + "]"
	}
	return `{"from":` + exts("f") + `,"to":` + exts("t") + `},`
}()

func TestParse_ConversionsAtTheirLimits(t *testing.T) {
	doc := `{"publisher":"abc","conversions":[` + strings.TrimSuffix(manyPairs, ",") + `]}`
	c, err := Parse([]byte(doc))
	if err != nil {
		t.Fatalf("exactly %d pairs: %v", MaxConversionPairs, err)
	}
	if Pairs(c.Conversions) != MaxConversionPairs {
		t.Errorf("pairs %d", Pairs(c.Conversions))
	}
	if _, err := Parse([]byte(`{"publisher":"abc","conversions":[{"from":["a"],"to":["b"],"needs":["a","b","c","d","e","f","g"]}]}`)); err == nil ||
		!strings.Contains(err.Error(), "conversions[0].needs: 1 to 6") {
		t.Errorf("seven engines: %v", err)
	}
	if _, err := Parse([]byte(`{"publisher":"abc","conversions":[{"from":["a"],"to":["` + strings.Repeat("b", 33) + `"]}]}`)); err == nil {
		t.Error("an extension part of 33 characters is not filex's grammar")
	}
	if _, err := Parse([]byte(`{"publisher":"abc","conversions":[{"from":["tar.gz","a.b.c"],"to":["zip"]}]}`)); err != nil {
		t.Errorf("dotted extensions of up to three parts: %v", err)
	}
}

// A conversion may only need an engine the manifest asks for: an
// engines:<id> permission or the manifest's engines list.
func TestCheckNeeds_OnlyEnginesTheManifestAsksFor(t *testing.T) {
	convs := []filetypes.Conversion{
		{From: []string{"docx"}, To: []string{"pdf"}},
		{From: []string{"doc"}, To: []string{"pdf"}, Needs: []string{"office"}},
		{From: []string{"mp4"}, To: []string{"webm"}, Needs: []string{"ffmpeg", "imagemagick"}},
	}
	perms := []string{"files:read", "files:write", "engines:office", "engines:ffmpeg"}
	got := CheckNeeds(convs, perms, nil)
	if len(got) != 1 || !strings.Contains(got[0], `conversions[2].needs[1]: "imagemagick" is not an engine the manifest asks for`) {
		t.Errorf("got %v", got)
	}
	if got := CheckNeeds(convs, perms, []string{"imagemagick"}); len(got) != 0 {
		t.Errorf("the manifest's engines list counts too: %v", got)
	}
	if got := CheckNeeds(convs, []string{"engines"}, nil); len(got) != 3 {
		t.Errorf("no engine at all: %v", got)
	}
}

// Every problem is listed, not only the first.
func TestParse_ListsEveryProblem(t *testing.T) {
	_, err := Parse([]byte(`{"publisher":"X","icon":"/a","categories":["A"]}`))
	var ce *Error
	if !errors.As(err, &ce) || len(ce.Problems) != 3 {
		t.Fatalf("got %v", err)
	}
}

// ⚠ The hand-written rules must say what the schema file says. When the
// schema changes (a contract commit), this test fails until the code here
// follows.
func TestRules_MatchTheSchemaFile(t *testing.T) {
	b, err := os.ReadFile("../../../schema/filex-apps.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var s struct {
		AdditionalProperties bool     `json:"additionalProperties"`
		Required             []string `json:"required"`
		Properties           map[string]struct {
			Pattern  string   `json:"pattern"`
			MaxItems int      `json:"maxItems"`
			Required []string `json:"required"`
			Items    struct {
				Pattern              string `json:"pattern"`
				AdditionalProperties *bool  `json:"additionalProperties"`
				Required             []string
				Properties           map[string]json.RawMessage `json:"properties"`
			} `json:"items"`
			UniqueItems bool `json:"uniqueItems"`
		} `json:"properties"`
		Defs struct {
			Path struct {
				MinLength int    `json:"minLength"`
				MaxLength int    `json:"maxLength"`
				Pattern   string `json:"pattern"`
			} `json:"path"`
			Text struct {
				PropertyNames struct {
					Pattern string `json:"pattern"`
				} `json:"propertyNames"`
				AdditionalProperties struct {
					MinLength int `json:"minLength"`
					MaxLength int `json:"maxLength"`
				} `json:"additionalProperties"`
			} `json:"text"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	eq := func(what string, got, want any) {
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: code has %v, schema has %v", what, got, want)
		}
	}
	eq("additionalProperties", false, s.AdditionalProperties)
	eq("required", []string{"publisher"}, s.Required)
	props := make([]string, 0, len(s.Properties))
	for k := range s.Properties {
		props = append(props, k)
	}
	for _, k := range props {
		switch k {
		case "publisher", "categories", "summary", "description", "icon", "screenshots", "conversions":
		default:
			t.Errorf("the schema has a property %q the code does not read", k)
		}
	}
	eq("publisher pattern", publisherRe.String(), s.Properties["publisher"].Pattern)
	eq("categories maxItems", MaxCategories, s.Properties["categories"].MaxItems)
	eq("categories uniqueItems", true, s.Properties["categories"].UniqueItems)
	eq("category pattern", categoryRe.String(), s.Properties["categories"].Items.Pattern)
	eq("summary required", []string{"en"}, s.Properties["summary"].Required)
	eq("screenshots maxItems", MaxScreenshots, s.Properties["screenshots"].MaxItems)
	eq("screenshot additionalProperties", false, *s.Properties["screenshots"].Items.AdditionalProperties)
	if len(s.Properties["screenshots"].Items.Properties) != 2 {
		t.Errorf("screenshot properties %v", s.Properties["screenshots"].Items.Properties)
	}
	eq("path minLength", 1, s.Defs.Path.MinLength)
	eq("path maxLength", MaxPathLen, s.Defs.Path.MaxLength)
	eq("path pattern", `^(?!/)(?!.*(^|/)\.\.(/|$))`+strings.TrimPrefix(pathChars.String(), "^"), s.Defs.Path.Pattern)
	eq("language pattern", langRe.String(), s.Defs.Text.PropertyNames.Pattern)
	eq("text minLength", 1, s.Defs.Text.AdditionalProperties.MinLength)
	eq("text maxLength", MaxTextLen, s.Defs.Text.AdditionalProperties.MaxLength)

	var c struct {
		Properties struct {
			Conversions struct {
				MinItems int `json:"minItems"`
				MaxItems int `json:"maxItems"`
				Items    struct {
					AdditionalProperties *bool    `json:"additionalProperties"`
					Required             []string `json:"required"`
					Properties           struct {
						From struct {
							Ref string `json:"$ref"`
						} `json:"from"`
						To struct {
							Ref string `json:"$ref"`
						} `json:"to"`
						Needs struct {
							MinItems    int  `json:"minItems"`
							MaxItems    int  `json:"maxItems"`
							UniqueItems bool `json:"uniqueItems"`
							Items       struct {
								Pattern string `json:"pattern"`
							} `json:"items"`
						} `json:"needs"`
					} `json:"properties"`
				} `json:"items"`
			} `json:"conversions"`
		} `json:"properties"`
		Defs struct {
			Exts struct {
				MinItems    int  `json:"minItems"`
				MaxItems    int  `json:"maxItems"`
				UniqueItems bool `json:"uniqueItems"`
				Items       struct {
					Pattern string `json:"pattern"`
				} `json:"items"`
			} `json:"exts"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	conv := c.Properties.Conversions
	eq("conversions minItems", 1, conv.MinItems)
	eq("conversions maxItems", MaxConversions, conv.MaxItems)
	eq("conversion additionalProperties", false, conv.Items.AdditionalProperties != nil && *conv.Items.AdditionalProperties)
	eq("conversion required", []string{"from", "to"}, conv.Items.Required)
	eq("conversion from", "#/$defs/exts", conv.Items.Properties.From.Ref)
	eq("conversion to", "#/$defs/exts", conv.Items.Properties.To.Ref)
	eq("needs minItems", 1, conv.Items.Properties.Needs.MinItems)
	eq("needs maxItems", MaxConversionNeeds, conv.Items.Properties.Needs.MaxItems)
	eq("needs uniqueItems", true, conv.Items.Properties.Needs.UniqueItems)
	eq("engine pattern", engineRe.String(), conv.Items.Properties.Needs.Items.Pattern)
	eq("exts minItems", 1, c.Defs.Exts.MinItems)
	eq("exts maxItems", MaxConversionExts, c.Defs.Exts.MaxItems)
	eq("exts uniqueItems", true, c.Defs.Exts.UniqueItems)
	eq("extension pattern", extRe.String(), c.Defs.Exts.Items.Pattern)
	eq("extension pattern is filex's", filetypes.ExtPattern, c.Defs.Exts.Items.Pattern)
	if !strings.Contains(string(b), fmt.Sprintf("At most %d from-to pairs", MaxConversionPairs)) {
		t.Errorf("the schema's conversions description does not name the %d-pair cap", MaxConversionPairs)
	}
}
