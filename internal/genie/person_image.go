package genie

import (
	"encoding/json"
	"log/slog"
	"strings"

	"asi-genie-poc/internal/cards"
)

// recordImage reads a profile photo from a user or guest record.
// Property users expose avatarURL; the signed-in user exposes image.
func recordImage(m map[string]any) string {
	return strings.TrimSpace(str(m, "avatarURL", "avatarUrl", "image", "imageUrl", "photoUrl", "profileImage"))
}

type personPhoto struct {
	names []string
	image string
}

// attachPersonImages copies profile photos from the raw reads onto cards.
// present_answer keeps labels and values, so a photo URL never reaches the panel
// unless we attach it here. A card with no photo stays empty and the panel
// shows the person's initials.
func attachPersonImages(cs []cards.Card, reads []readResult) {
	people := peopleFromReads(reads)
	for i := range cs {
		liftImageRow(&cs[i])
		stripPhotoColumns(cs[i].Table)
		if cs[i].Image == "" {
			if img, ok := photoFor(cardLabels(&cs[i]), people); ok {
				cs[i].Image = img
			}
		}
		for j := range cs[i].Items {
			if cs[i].Items[j].Image != "" {
				continue
			}
			if img, ok := photoFor([]string{cs[i].Items[j].Title, cs[i].Items[j].Subtitle}, people); ok {
				cs[i].Items[j].Image = img
			}
		}
		if cs[i].Table == nil {
			continue
		}
		for _, row := range cs[i].Table.Rows {
			if strings.TrimSpace(row["image"]) != "" {
				continue
			}
			labels := make([]string, 0, len(row))
			for k, v := range row {
				if k == "image" {
					continue
				}
				labels = append(labels, v)
			}
			if img, ok := photoFor(labels, people); ok {
				row["image"] = img
			}
		}
	}
}

func cardLabels(c *cards.Card) []string {
	labels := []string{c.Title, c.Subtitle}
	for _, row := range c.Rows {
		if isNameLabel(row.Label) {
			labels = append(labels, row.Value)
		}
	}
	return labels
}

func isNameLabel(label string) bool {
	switch strings.ToLower(strings.TrimSpace(label)) {
	case "name", "full name", "guest", "guest name", "user", "username", "email":
		return true
	default:
		return false
	}
}

// liftImageRow moves a photo URL out of a visible field so the card header
// can show it and the row list does not print the raw address.
func liftImageRow(c *cards.Card) {
	if c.Image != "" || len(c.Rows) == 0 {
		return
	}
	kept := make([]cards.Field, 0, len(c.Rows))
	for _, row := range c.Rows {
		if c.Image == "" && isImageLabel(row.Label) && isImageValue(row.Value) {
			c.Image = strings.TrimSpace(row.Value)
			continue
		}
		kept = append(kept, row)
	}
	c.Rows = kept
}

func isImageLabel(label string) bool {
	l := strings.ToLower(strings.TrimSpace(label))
	l = strings.ReplaceAll(l, " ", "")
	l = strings.ReplaceAll(l, "_", "")
	l = strings.ReplaceAll(l, "-", "")
	switch l {
	case "avatar", "avatarurl", "image", "imageurl", "photo", "photourl", "profilephoto", "profileimage", "profilepicture":
		return true
	default:
		return false
	}
}

func isImageValue(v string) bool {
	s := strings.TrimSpace(v)
	if s == "" || s == "-" || strings.EqualFold(s, "no") || strings.EqualFold(s, "n/a") || strings.EqualFold(s, "none") {
		return false
	}
	return strings.Contains(s, "/") || strings.Contains(s, ".")
}

// stripPhotoColumns keeps a photo URL off the grid and on row["image"],
// which the panel draws as an avatar instead of a text cell.
func stripPhotoColumns(t *cards.TableData) {
	if t == nil {
		return
	}
	var cols []cards.Column
	var photoKeys []string
	for _, c := range t.Columns {
		if isImageLabel(c.Label) || isImageLabel(c.Key) {
			photoKeys = append(photoKeys, c.Key)
			continue
		}
		cols = append(cols, c)
	}
	if len(photoKeys) == 0 {
		return
	}
	t.Columns = cols
	for _, row := range t.Rows {
		if strings.TrimSpace(row["image"]) == "" {
			for _, k := range photoKeys {
				if isImageValue(row[k]) {
					row["image"] = strings.TrimSpace(row[k])
					break
				}
			}
		}
		for _, k := range photoKeys {
			delete(row, k)
		}
	}
}

func peopleFromReads(reads []readResult) []personPhoto {
	var out []personPhoto
	for _, r := range reads {
		if len(r.Data) == 0 {
			continue
		}
		var v any
		if err := json.Unmarshal(r.Data, &v); err != nil {
			slog.Warn("genie person image skipped unreadable read", "err", err, "tool", r.Tool)
			continue
		}
		walkPeople(v, 0, &out)
	}
	return out
}

func walkPeople(v any, depth int, out *[]personPhoto) {
	if depth > 8 || v == nil {
		return
	}
	switch t := v.(type) {
	case map[string]any:
		if img := recordImage(t); img != "" {
			names := personLabels(t)
			if len(names) > 0 {
				*out = append(*out, personPhoto{names: names, image: img})
			}
		}
		for _, child := range t {
			walkPeople(child, depth+1, out)
		}
	case []any:
		for _, child := range t {
			walkPeople(child, depth+1, out)
		}
	}
}

func personLabels(m map[string]any) []string {
	var names []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" {
			return
		}
		names = append(names, s)
	}
	add(personName(m))
	add(str(m, "userName", "username"))
	add(str(m, "email"))
	return names
}

// photoFor returns a photo only when exactly one person matches the labels.
// Two staff with the same name stay initials rather than showing the wrong photo.
func photoFor(labels []string, people []personPhoto) (string, bool) {
	var hit string
	for _, p := range people {
		if !personMatches(labels, p.names) {
			continue
		}
		if hit != "" && hit != p.image {
			return "", false
		}
		hit = p.image
	}
	if hit == "" {
		return "", false
	}
	return hit, true
}

func personMatches(labels, names []string) bool {
	for _, label := range labels {
		for _, name := range names {
			if namesMatch(label, name) {
				return true
			}
		}
	}
	return false
}

func namesMatch(label, name string) bool {
	h := normName(label)
	n := normName(name)
	if len(n) < 3 || h == "" {
		return false
	}
	if h == n {
		return true
	}
	// The card title is often the question ("Who is Sunny Sahsi?"), so the
	// person's name has to appear as a whole phrase inside it.
	return strings.Contains(" "+h+" ", " "+n+" ")
}

func normName(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}
