package civitai

import "testing"

// realImagesBody is a trimmed but SHAPE-FAITHFUL slice of a real
// /api/v1/images?modelVersionId=3112728&nsfw=X response (captured 2026-07-31).
//
// The two "X" entries are the point: identical nsfwLevel strings, different
// browsingLevel numbers. A decoder that only reads nsfwLevel cannot tell them
// apart.
const realImagesBody = `{"items":[
 {"id":1,"url":"https://image.civitai.com/a/width=450/a.jpeg","nsfwLevel":"None","browsingLevel":1,"nsfw":false,"width":832,"height":1216,"username":"alice","stats":{"likeCount":1}},
 {"id":2,"url":"https://image.civitai.com/b/width=450/b.jpeg","nsfwLevel":"Soft","browsingLevel":2,"nsfw":false,"width":832,"height":1216,"username":"bob","stats":{"likeCount":2}},
 {"id":3,"url":"https://image.civitai.com/c/width=450/c.jpeg","nsfwLevel":"Mature","browsingLevel":4,"nsfw":true,"width":832,"height":1216,"username":"carol","stats":{"likeCount":3}},
 {"id":4,"url":"https://image.civitai.com/d/width=450/d.jpeg","nsfwLevel":"X","browsingLevel":8,"nsfw":true,"width":832,"height":1216,"username":"dave","stats":{"likeCount":4}},
 {"id":5,"url":"https://image.civitai.com/e/width=450/e.jpeg","nsfwLevel":"X","browsingLevel":16,"nsfw":true,"width":832,"height":1216,"username":"erin","stats":{"likeCount":5}}
],"metadata":{"nextCursor":"x"}}`

func TestDecodeLeveledImageSearchCarriesTheNumericLevel(t *testing.T) {
	res, err := DecodeLeveledImageSearch([]byte(realImagesBody))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(res.Items) != 5 {
		t.Fatalf("got %d items, want 5", len(res.Items))
	}
	wantLevels := []int{1, 2, 4, 8, 16}
	for i, it := range res.Items {
		if it.BrowsingLevel != wantLevels[i] {
			t.Errorf("item %d browsingLevel = %d, want %d", it.ID, it.BrowsingLevel, wantLevels[i])
		}
		// The embedded ImageItem fields must still decode (the promotion is what
		// lets the render path keep using URL/Username/Stats).
		if it.URL == "" || it.Username == "" {
			t.Errorf("item %d lost its embedded ImageItem fields (%+v)", it.ID, it.ImageItem)
		}
	}
	if res.Items[3].NSFWLevel != res.Items[4].NSFWLevel {
		t.Fatalf("fixture is not exercising the label collapse: %q vs %q",
			res.Items[3].NSFWLevel, res.Items[4].NSFWLevel)
	}
	if res.Items[3].BrowsingLevel == res.Items[4].BrowsingLevel {
		t.Errorf("X and XXX decoded to the same browsingLevel — the collapse was not avoided")
	}
	if string(res.Raw) != realImagesBody {
		t.Errorf("Raw was not preserved")
	}
}

func TestDecodeLeveledImageSearchRejectsUnusableBodies(t *testing.T) {
	for _, raw := range [][]byte{nil, {}, []byte("not json"), []byte(`{"items":3}`)} {
		if _, err := DecodeLeveledImageSearch(raw); err == nil {
			t.Errorf("DecodeLeveledImageSearch(%q) accepted an unusable body", raw)
		}
	}
}

// TestDecodeLeveledImageSearchAbsentLevelIsZero: an item with no browsingLevel
// decodes to 0, which the web layer's maturity scale treats as unknown and omits
// (fail-closed) rather than guessing from the string label.
func TestDecodeLeveledImageSearchAbsentLevelIsZero(t *testing.T) {
	res, err := DecodeLeveledImageSearch([]byte(`{"items":[{"id":9,"url":"u","nsfwLevel":"X"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.Items[0].BrowsingLevel != 0 {
		t.Errorf("absent browsingLevel = %d, want 0", res.Items[0].BrowsingLevel)
	}
}
