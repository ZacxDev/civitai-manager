package poller

import (
	"reflect"
	"testing"
)

func candidateIDs(cs []Candidate) []int {
	var out []int
	for _, c := range cs {
		out = append(out, c.VersionID)
	}
	return out
}

func TestDiff(t *testing.T) {
	cands := []Candidate{
		{VersionID: 300}, {VersionID: 200}, {VersionID: 100},
	}
	cases := []struct {
		name string
		seen map[int]bool
		want []int
	}{
		{"none seen -> all new (order preserved)", map[int]bool{}, []int{300, 200, 100}},
		{"all seen -> none new", map[int]bool{100: true, 200: true, 300: true}, nil},
		{"one new at head", map[int]bool{200: true, 100: true}, []int{300}},
		{"gap in middle", map[int]bool{300: true, 100: true}, []int{200}},
		{"unknown seen ids ignored", map[int]bool{999: true}, []int{300, 200, 100}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := candidateIDs(Diff(c.seen, cands))
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("Diff = %v, want %v", got, c.want)
			}
		})
	}
}

func TestCandidatesFromCreatorSearch(t *testing.T) {
	raw := []byte(`{"items":[
		{"id":1,"name":"Model A","type":"LORA","creator":{"username":"alice"},
		 "modelVersions":[{"id":11,"name":"v2","baseModel":"SDXL"},{"id":10,"name":"v1","baseModel":"SDXL"}]},
		{"id":2,"name":"Model B","type":"Checkpoint",
		 "modelVersions":[{"id":20,"name":"v1","baseModel":"SD 1.5"}]}
	]}`)
	got, err := candidatesFromCreatorSearch(raw, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d candidates, want 3", len(got))
	}
	if got[0].VersionID != 11 || got[0].ModelID != 1 || got[0].CreatorUsername != "alice" {
		t.Errorf("candidate[0] wrong: %+v", got[0])
	}
	// Model B has no creator block: falls back to the username argument.
	if got[2].VersionID != 20 || got[2].CreatorUsername != "alice" || got[2].BaseModel != "SD 1.5" {
		t.Errorf("candidate[2] wrong: %+v", got[2])
	}
}
