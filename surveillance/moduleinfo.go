package main

// ModuleInfo mirrors the ArozOS modules.ModuleInfo struct field-for-field so
// that the JSON printed by `-info` (and the shipped moduleInfo.json) registers
// this subservice as a normal desktop module. See the "What a SubService is"
// section of the repository CLAUDE.md for the handshake details.
type ModuleInfo struct {
	Name         string   `json:"Name"`
	Desc         string   `json:"Desc"`
	Group        string   `json:"Group"`
	IconPath     string   `json:"IconPath"`
	Version      string   `json:"Version"`
	StartDir     string   `json:"StartDir"`
	SupportFW    bool     `json:"SupportFW"`
	LaunchFWDir  string   `json:"LaunchFWDir"`
	SupportEmb   bool     `json:"SupportEmb"`
	LaunchEmb    string   `json:"LaunchEmb"`
	InitFWSize   []int    `json:"InitFWSize"`
	InitEmbSize  []int    `json:"InitEmbSize"`
	SupportedExt []string `json:"SupportedExt"`
}

// moduleInfo is the metadata ArozOS reads to place this subservice on the
// desktop. The directory part of StartDir ("surveillance") becomes the
// reverse-proxy endpoint, so every route this binary serves is namespaced
// under /surveillance/.
func moduleInfo() ModuleInfo {
	return ModuleInfo{
		Name:         "Surveillance",
		Desc:         "Connect, monitor and manage IP cameras (RTSP) from a central web interface",
		Group:        "System Tools",
		IconPath:     "surveillance/img/icon.svg",
		Version:      version,
		StartDir:     "surveillance/index.html",
		SupportFW:    true,
		LaunchFWDir:  "surveillance/index.html",
		SupportEmb:   false,
		InitFWSize:   []int{1280, 800},
		InitEmbSize:  []int{960, 600},
		SupportedExt: []string{},
	}
}
