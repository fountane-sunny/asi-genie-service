package genie

import (
	"encoding/json"
	"testing"

	"asi-genie-poc/internal/cards"
)

func TestAttachPersonImagesUsesAvatarURL(t *testing.T) {
	payload, err := json.Marshal(map[string]any{
		"data": []any{
			map[string]any{
				"fullName":  "Sunny Sahsi",
				"userName":  "sahsisunny",
				"email":     "sunny.sahsi@fountane.com",
				"avatarURL": "https://cdn.example/sunny.jpg",
			},
			map[string]any{
				"fullName": "Nena Sahsi",
				"userName": "nenasahsi",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	cs := []cards.Card{{
		Type:  cards.TypeData,
		Title: "Who is Sunny Sahsi?",
		Rows: []cards.Field{
			{Label: "Full name", Value: "Sunny Sahsi"},
			{Label: "Username", Value: "sahsisunny"},
		},
		Items: []cards.ListItem{{Title: "Nena Sahsi"}, {Title: "Sunny Sahsi"}},
		Table: &cards.TableData{
			Columns: []cards.Column{{Key: "name", Label: "Name"}, {Key: "avatarURL", Label: "Photo"}},
			Rows: []map[string]string{
				{"name": "Sunny Sahsi", "avatarURL": "https://cdn.example/sunny.jpg"},
			},
		},
	}, {
		Title: "Photo row",
		Rows:  []cards.Field{{Label: "Avatar", Value: "https://cdn.example/row.jpg"}, {Label: "Role", Value: "Owner"}},
	}}
	attachPersonImages(cs, []readResult{{Tool: "config_get_properties_id_users", Data: payload}})
	if cs[0].Image != "https://cdn.example/sunny.jpg" {
		t.Fatalf("card image = %q", cs[0].Image)
	}
	for _, row := range cs[0].Rows {
		if row.Label == "Avatar" {
			t.Fatalf("photo URL row should be lifted off the card, rows = %#v", cs[0].Rows)
		}
	}
	if cs[0].Items[0].Image != "" {
		t.Fatalf("Nena has no photo, image = %q", cs[0].Items[0].Image)
	}
	if cs[0].Items[1].Image != "https://cdn.example/sunny.jpg" {
		t.Fatalf("list photo = %q", cs[0].Items[1].Image)
	}
	if cs[1].Image != "https://cdn.example/row.jpg" || len(cs[1].Rows) != 1 {
		t.Fatalf("lifted photo card = %#v", cs[1])
	}
	if len(cs[0].Table.Columns) != 1 || cs[0].Table.Columns[0].Key != "name" {
		t.Fatalf("photo column should be hidden, columns = %#v", cs[0].Table.Columns)
	}
	if cs[0].Table.Rows[0]["image"] != "https://cdn.example/sunny.jpg" {
		t.Fatalf("table row image = %#v", cs[0].Table.Rows[0])
	}
}

func TestAttachPersonImagesSkipsAmbiguousNames(t *testing.T) {
	payload, err := json.Marshal(map[string]any{
		"data": []any{
			map[string]any{"fullName": "Sunny Sahsi", "avatarURL": "https://cdn.example/owner.jpg"},
			map[string]any{"fullName": "Sunny Sahsi", "avatarURL": "https://cdn.example/desk.jpg"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	cs := []cards.Card{{Title: "Sunny Sahsi", Rows: []cards.Field{{Label: "Role", Value: "Owner"}}}}
	attachPersonImages(cs, []readResult{{Data: payload}})
	if cs[0].Image != "" {
		t.Fatalf("two photos for one name should stay initials, image = %q", cs[0].Image)
	}
}

func TestRecordImageReadsSignedInUserField(t *testing.T) {
	if got := recordImage(map[string]any{"image": " https://cdn.example/me.png "}); got != "https://cdn.example/me.png" {
		t.Fatalf("image = %q", got)
	}
	if got := recordImage(map[string]any{"avatarURL": ""}); got != "" {
		t.Fatalf("empty avatar = %q", got)
	}
}
