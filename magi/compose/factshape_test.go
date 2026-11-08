package compose

import "testing"

func TestShapeFact(t *testing.T) {
	f, more := shapeFact(fact{subject: "me", relation: "vet", object: "Dr. Okafor at Birch Lane Animal Clinic"})
	if f.object != "Dr. Okafor" || f.objectKind != "person" || len(more) != 1 ||
		more[0].subject != "Dr. Okafor" || more[0].relation != "works-at" || more[0].object != "Birch Lane Animal Clinic" {
		t.Fatalf("at place: %+v %+v", f, more)
	}
	if f, _ := shapeFact(fact{subject: "my work calendar", relation: "located-in", object: "company Google Workspace"}); f.subject != "work calendar" || f.object != "company Google Workspace" {
		t.Fatalf("possessive: %+v", f)
	}
	if f, _ := shapeFact(fact{subject: "Dana", relation: "birthday", object: "March 3", objectKind: "account"}); f.object != "" || f.value != "March 3" || f.objectKind != "" {
		t.Fatalf("date: %+v", f)
	}
	if f, more := shapeFact(fact{subject: "me", relation: "dentist", object: "The Smile Center"}); f.object != "The Smile Center" || len(more) != 0 {
		t.Fatalf("proper The: %+v %+v", f, more)
	}
	if f, more := shapeFact(fact{subject: "Dr. Lee", relation: "works-at", object: "Elm Clinic at Main Street"}); f.object != "Elm Clinic at Main Street" || len(more) != 0 {
		t.Fatalf("works-at stays: %+v %+v", f, more)
	}
	if f, _ := shapeFact(fact{subject: "me", relation: "job", object: "Brightline Logistics"}); f.relation != "works-at" {
		t.Fatalf("job: %+v", f)
	}
	if f, _ := shapeFact(fact{subject: "me", relation: "job", value: "operations manager"}); f.relation != "job" {
		t.Fatalf("job title: %+v", f)
	}
	if f, _ := shapeFact(fact{subject: "family calendar", relation: "located-in", value: "the household Nextcloud"}); f.object != "household Nextcloud" || f.value != "" {
		t.Fatalf("thing value: %+v", f)
	}
	if f, _ := shapeFact(fact{subject: "Dana", relation: "birthday", value: "March 3"}); f.value != "March 3" {
		t.Fatalf("value stays: %+v", f)
	}
	if f, _ := shapeFact(fact{subject: "me", relation: "router", object: "Ubiquiti Dream Machine"}); f.subject != "me" {
		t.Fatalf("self: %+v", f)
	}
}

func TestCoreName(t *testing.T) {
	for name, want := range map[string]string{
		"household Nextcloud":      "nextcloud",
		"Nextcloud":                "nextcloud",
		"company Google Workspace": "google workspace",
		"family calendar":          "",
		"Dr. Okafor":               "dr. okafor",
	} {
		if got := coreName(name); got != want {
			t.Errorf("%q: %q, want %q", name, got, want)
		}
	}
}
