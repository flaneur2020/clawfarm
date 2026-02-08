package state

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const metadataFileName = "instance.json"

var ErrNotFound = errors.New("instance not found")

type PortMapping struct {
	HostPort  int `json:"host_port"`
	GuestPort int `json:"guest_port"`
}

type Instance struct {
	ID             string        `json:"id"`
	ImageRef       string        `json:"image_ref"`
	WorkspacePath  string        `json:"workspace_path"`
	StatePath      string        `json:"state_path"`
	GatewayPort    int           `json:"gateway_port"`
	PublishedPorts []PortMapping `json:"published_ports"`
	Status         string        `json:"status"`
	CreatedAtUTC   time.Time     `json:"created_at_utc"`
	UpdatedAtUTC   time.Time     `json:"updated_at_utc"`
}

type Store struct {
	root string
}

func NewStore(root string) *Store {
	return &Store{root: root}
}

func (s *Store) Save(instance Instance) error {
	dir := filepath.Join(s.root, instance.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	file, err := os.Create(filepath.Join(dir, metadataFileName))
	if err != nil {
		return err
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	return encoder.Encode(instance)
}

func (s *Store) Load(id string) (Instance, error) {
	file, err := os.Open(filepath.Join(s.root, id, metadataFileName))
	if err != nil {
		if os.IsNotExist(err) {
			return Instance{}, ErrNotFound
		}
		return Instance{}, err
	}
	defer file.Close()

	var instance Instance
	if err := json.NewDecoder(file).Decode(&instance); err != nil {
		return Instance{}, err
	}
	return instance, nil
}

func (s *Store) List() ([]Instance, error) {
	if err := os.MkdirAll(s.root, 0o755); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return nil, err
	}

	instances := make([]Instance, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		instance, err := s.Load(entry.Name())
		if err != nil {
			continue
		}
		instances = append(instances, instance)
	}

	sort.Slice(instances, func(i, j int) bool {
		if instances[i].CreatedAtUTC.Equal(instances[j].CreatedAtUTC) {
			return instances[i].ID < instances[j].ID
		}
		return instances[i].CreatedAtUTC.After(instances[j].CreatedAtUTC)
	})

	return instances, nil
}

func (s *Store) Delete(id string) error {
	dir := filepath.Join(s.root, id)
	if _, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		return err
	}
	return os.RemoveAll(dir)
}
