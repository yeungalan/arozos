package main

import _ "embed"

/*
	cascade.go

	The pigo "facefinder" classifier cascade is embedded directly into the
	binary so the subservice stays a single self-contained executable with no
	external asset to deploy. The cascade ships with github.com/esimov/pigo
	(MIT licensed, (c) 2018 Endre Simo); see testdata/NOTICE.md.
*/

//go:embed facefinder
var faceFinderCascade []byte
