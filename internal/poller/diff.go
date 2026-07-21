package poller

import (
	"encoding/json"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
)

// Candidate is one model-version discovered during a poll, flattened to the
// fields the poller needs to diff, notify on, lay out on disk, and filter. Both
// the model-sub and creator-sub fetch paths produce a slice of these in
// newest-first order.
type Candidate struct {
	ModelID         int
	VersionID       int
	ModelName       string
	ModelType       string
	CreatorUsername string
	VersionName     string
	BaseModel       string
}

// Diff returns the candidates whose version id is not in the seen set,
// preserving input order (newest first). It is a pure function -- the unit of
// the version-diff test suite.
func Diff(seen map[int]bool, candidates []Candidate) []Candidate {
	var out []Candidate
	for _, c := range candidates {
		if !seen[c.VersionID] {
			out = append(out, c)
		}
	}
	return out
}

// candidatesFromModel flattens a model detail's version summaries into
// candidates. The API returns modelVersions newest-first.
func candidatesFromModel(m *civitai.ModelDetail) []Candidate {
	if m == nil {
		return nil
	}
	creator := ""
	if m.Creator != nil {
		creator = m.Creator.Username
	}
	out := make([]Candidate, 0, len(m.ModelVersions))
	for _, v := range m.ModelVersions {
		out = append(out, Candidate{
			ModelID:         m.ID,
			VersionID:       v.ID,
			ModelName:       m.Name,
			ModelType:       m.Type,
			CreatorUsername: creator,
			VersionName:     v.Name,
			BaseModel:       v.BaseModel,
		})
	}
	return out
}

// rawModelsSearch is the minimal shape parsed from a models-search raw body.
// The typed ModelListItem does not carry versions, but the raw list payload
// embeds modelVersions[] per model -- which is what a creator poll diffs on.
type rawModelsSearch struct {
	Items []struct {
		ID      int    `json:"id"`
		Name    string `json:"name"`
		Type    string `json:"type"`
		Creator *struct {
			Username string `json:"username"`
		} `json:"creator"`
		ModelVersions []struct {
			ID        int    `json:"id"`
			Name      string `json:"name"`
			BaseModel string `json:"baseModel"`
		} `json:"modelVersions"`
	} `json:"items"`
}

// candidatesFromCreatorSearch flattens a models-search raw body (a creator's
// models, newest-first) into candidates across every model's versions. It falls
// back to the username argument when a model item omits its creator block.
func candidatesFromCreatorSearch(raw []byte, username string) ([]Candidate, error) {
	var body rawModelsSearch
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, err
	}
	var out []Candidate
	for _, it := range body.Items {
		creator := username
		if it.Creator != nil && it.Creator.Username != "" {
			creator = it.Creator.Username
		}
		for _, v := range it.ModelVersions {
			out = append(out, Candidate{
				ModelID:         it.ID,
				VersionID:       v.ID,
				ModelName:       it.Name,
				ModelType:       it.Type,
				CreatorUsername: creator,
				VersionName:     v.Name,
				BaseModel:       v.BaseModel,
			})
		}
	}
	return out, nil
}
