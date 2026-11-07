package demo

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"strings"
)

type Scenario struct {
	Post    Post
	Replies map[string]Reply
}

// LoadScenario resolves a scenario against the checked-in marketplace seeds.
// Posts are published through Buyer, so loading a fixture has no side effects.
func LoadScenario(files fs.FS, name string) (Scenario, error) {
	if name == "." || !fs.ValidPath(name) || strings.Contains(name, "/") {
		return Scenario{}, fmt.Errorf("invalid demo scenario name %q", name)
	}
	var definition struct {
		PostSeed string           `json:"post_seed"`
		Replies  map[string]Reply `json:"replies"`
	}
	if err := readFixture(files, "testdata/scenarios/"+name+".json", &definition); err != nil {
		return Scenario{}, err
	}
	var seeds map[string]Post
	if err := readFixture(files, "testdata/seeds/marketplace.json", &seeds); err != nil {
		return Scenario{}, err
	}
	post, ok := seeds[definition.PostSeed]
	if !ok {
		return Scenario{}, fmt.Errorf("demo scenario %q references unknown post seed %q", name, definition.PostSeed)
	}
	return Scenario{Post: post, Replies: definition.Replies}, nil
}

func readFixture(files fs.FS, path string, target any) error {
	body, err := fs.ReadFile(files, path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, target); err != nil {
		return fmt.Errorf("decode fixture %s: %w", path, err)
	}
	return nil
}
