package filetypes

import (
	"reflect"
	"sort"
	"strings"
)

// RoleConvert: the version's listing declares that it converts the kind
// into others (`conversions` in .filex-apps.json, reviewed with the code and
// held to schema/filex-apps.schema.json by internal/consent). Unlike the
// other roles it is not read from the manifest: an action offered on every
// file (filex-convert's "Convert…") names no kind, and only its listing can
// say which kinds it turns into which.
const RoleConvert Role = "convert"

// Conversion is one entry of a listing's `conversions`: a file of every From
// extension converts into every To extension, needing every engine of Needs
// on the filex it runs in (ids of the manifest's `engines:<id>` permissions;
// "office" is the ONLYOFFICE Document Server connected to filex). No Needs:
// it needs nothing else. A pair that works with any one of several engines
// is declared once per engine.
type Conversion struct {
	From  []string `json:"from"`
	To    []string `json:"to"`
	Needs []string `json:"needs,omitempty"`
}

// ConvertsFrom is every extension the set's conversions start from, sorted,
// once each (a bare word that is one of them reads as a file-type search).
func (s Set) ConvertsFrom() []string {
	seen := map[string]bool{}
	out := []string{}
	for _, c := range s.Conversions {
		for _, e := range c.From {
			if !seen[e] {
				seen[e] = true
				out = append(out, e)
			}
		}
	}
	sort.Strings(out)
	return out
}

// convertUses is what the conversions do with files of extension ext: one
// RoleConvert entry per condition, each with its targets in the order the
// listing first names them. A target that works with nothing else is listed
// only there (whatever engine would also do it); otherwise its condition is
// every smallest engine set the listing gives for it (a set that holds
// another one is dropped), and targets with the same condition are one
// entry. The entry that needs nothing comes first, the others follow in the
// order their first target appears.
func convertUses(convs []Conversion, ext string) []RoleUse {
	var order []string
	alts := map[string][][]string{}
	for _, c := range convs {
		if !contains(c.From, ext) {
			continue
		}
		for _, t := range c.To {
			if t == ext {
				continue
			}
			if _, ok := alts[t]; !ok {
				order = append(order, t)
			}
			alts[t] = append(alts[t], c.Needs)
		}
	}
	type group struct {
		key   string
		needs [][]string
		to    []string
	}
	var groups []*group
	byKey := map[string]*group{}
	for _, t := range order {
		needs := minimalNeeds(alts[t])
		key := needsKey(needs)
		g, ok := byKey[key]
		if !ok {
			g = &group{key: key, needs: needs}
			byKey[key] = g
			groups = append(groups, g)
		}
		g.to = append(g.to, t)
	}
	sort.SliceStable(groups, func(i, j int) bool { return groups[i].key == "" && groups[j].key != "" })
	out := []RoleUse{}
	for _, g := range groups {
		out = append(out, RoleUse{Role: RoleConvert, To: g.to, NeedsAny: g.needs})
	}
	return out
}

// minimalNeeds reduces the engine sets that each make a conversion work to
// the smallest ones: nil when one of them is empty (it needs nothing), else
// the sets no other set is inside of, each sorted, in sorted order.
func minimalNeeds(sets [][]string) [][]string {
	var norm [][]string
	for _, s := range sets {
		if len(s) == 0 {
			return nil
		}
		c := append([]string{}, s...)
		sort.Strings(c)
		norm = append(norm, dedupe(c))
	}
	var out [][]string
	for i, s := range norm {
		redundant := false
		for j, o := range norm {
			if i == j {
				continue
			}
			if subset(o, s) && (len(o) < len(s) || (len(o) == len(s) && j < i)) {
				redundant = true
				break
			}
		}
		if !redundant {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return strings.Join(out[i], "+") < strings.Join(out[j], "+") })
	return out
}

func needsKey(needs [][]string) string {
	parts := make([]string, 0, len(needs))
	for _, s := range needs {
		parts = append(parts, strings.Join(s, "+"))
	}
	return strings.Join(parts, "|")
}

// subset reports whether every element of a is in b (both sorted, unique).
func subset(a, b []string) bool {
	for _, x := range a {
		if !contains(b, x) {
			return false
		}
	}
	return true
}

func dedupe(sorted []string) []string {
	out := sorted[:0]
	for i, v := range sorted {
		if i == 0 || v != sorted[i-1] {
			out = append(out, v)
		}
	}
	return out
}

// ConversionRow is one row of an app page's conversion table: the
// extensions that convert alike (an alias like jpeg beside jpg), and what
// they convert into, as the file-type search says it for each of them.
type ConversionRow struct {
	From  []string  `json:"from"`
	Roles []RoleUse `json:"roles"`
}

// ConversionTable is the set's conversions kind by kind, for an app's page:
// every extension a conversion starts from, in the order the listing first
// names it, with the extensions whose conversions are the same on one row.
func ConversionTable(s Set) []ConversionRow {
	out := []ConversionRow{}
	seen := map[string]bool{}
	for _, c := range s.Conversions {
		for _, e := range c.From {
			if seen[e] {
				continue
			}
			seen[e] = true
			uses := convertUses(s.Conversions, e)
			merged := false
			for i := range out {
				if reflect.DeepEqual(out[i].Roles, uses) {
					out[i].From = append(out[i].From, e)
					merged = true
					break
				}
			}
			if !merged {
				out = append(out, ConversionRow{From: []string{e}, Roles: uses})
			}
		}
	}
	return out
}
