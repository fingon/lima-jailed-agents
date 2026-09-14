package lja

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"gotest.tools/v3/assert"
)

const (
	safetyTestUID  = 1234
	safetyOtherUID = 4321
)

type ownedFileInfo struct {
	os.FileInfo
	owner uint32
}

func (info ownedFileInfo) Sys() any { return &syscall.Stat_t{Uid: info.owner} }

func TestDiscoveryDirectoryBoundaries(t *testing.T) {
	for _, test := range []struct {
		name            string
		homeBoundary    bool
		foreignBoundary bool
		foreignStart    bool
		marker          bool
		inspectionError bool
	}{
		{name: "home marker", homeBoundary: true, marker: true},
		{name: "home VM", homeBoundary: true},
		{name: "foreign marker", foreignBoundary: true, marker: true},
		{name: "foreign VM", foreignBoundary: true},
		{name: "owned marker", marker: true},
		{name: "owned VM"},
		{name: "foreign start", foreignStart: true, marker: true},
		{name: "inspection error", inspectionError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, err := canonicalPath(t.TempDir())
			assert.NilError(t, err)
			boundary := filepath.Join(root, "boundary")
			start := filepath.Join(boundary, "project")
			assert.NilError(t, os.MkdirAll(start, 0o755))
			if test.marker {
				assert.NilError(t, os.WriteFile(filepath.Join(boundary, projectConfigName), []byte("{}\n"), 0o600))
			}
			vmName, err := projectVMName(boundary)
			assert.NilError(t, err)
			command := filepath.Join(root, "limactl")
			assert.NilError(t, os.WriteFile(command, []byte("#!/bin/sh\nprintf '%s\\n' '[{\"name\":\""+vmName+"\",\"status\":\"Stopped\",\"config\":{\"mounts\":[]}}]'\n"), 0o755))
			policy := projectDirectoryPolicy{home: filepath.Join(root, "home"), uid: safetyTestUID}
			if test.homeBoundary {
				policy.home = boundary
			}
			policy.stat = func(path string) (os.FileInfo, error) {
				if path == boundary && test.inspectionError {
					return nil, os.ErrPermission
				}
				info, err := os.Stat(path)
				if err != nil {
					return nil, err
				}
				owner := uint32(safetyTestUID)
				if (path == boundary && test.foreignBoundary) || (path == start && test.foreignStart) {
					owner = safetyOtherUID
				}
				return ownedFileInfo{FileInfo: info, owner: owner}, nil
			}
			result, err := policy.discover(start, command)
			switch {
			case test.foreignStart:
				assert.ErrorContains(t, err, "not owned")
			case test.inspectionError:
				assert.ErrorIs(t, err, os.ErrPermission)
			default:
				assert.NilError(t, err)
				expected := boundary
				if test.homeBoundary || test.foreignBoundary {
					expected = start
				}
				assert.Equal(t, result, expected)
			}
		})
	}
}

func TestUnsafeHomeRejectedBeforePreparation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	alias := filepath.Join(t.TempDir(), "home-alias")
	assert.NilError(t, os.Symlink(home, alias))
	for _, project := range []string{home, alias} {
		t.Run(filepath.Base(project), func(t *testing.T) {
			assert.ErrorContains(t, validateProjectDirectory(project), "home directory")
			_, err := DiscoverProject(project, filepath.Join(home, "missing-limactl"))
			assert.ErrorContains(t, err, "home directory")
			_, err = PrepareVM(project, WorkflowOptions{LockDirectory: filepath.Join(home, "locks")})
			assert.ErrorContains(t, err, "home directory")
			_, err = InstallAgent(project, codexAgentName, false, WorkflowOptions{})
			assert.ErrorContains(t, err, "home directory")
			_, err = PrepareAgents(project, codexAgentName, nil, nil, WorkflowOptions{})
			assert.ErrorContains(t, err, "home directory")
			_, err = prepareNamedVMLocked(project, "unused", "", nil, nil, nil, "", filepath.Join(home, "missing-limactl"), "", false)
			assert.ErrorContains(t, err, "home directory")
			_, err = os.Stat(filepath.Join(home, "locks"))
			assert.ErrorIs(t, err, os.ErrNotExist)
		})
	}
}

func TestDirectoryPolicyInspection(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "file")
	assert.NilError(t, os.WriteFile(file, nil, 0o600))
	for _, test := range []struct {
		name  string
		path  string
		owner uint32
		want  string
	}{
		{name: "owned", path: root, owner: safetyTestUID},
		{name: "foreign", path: root, owner: safetyOtherUID, want: "not owned"},
		{name: "file", path: file, owner: safetyTestUID, want: "not a directory"},
	} {
		t.Run(test.name, func(t *testing.T) {
			policy := projectDirectoryPolicy{uid: safetyTestUID, stat: func(path string) (os.FileInfo, error) {
				info, err := os.Stat(path)
				if err != nil {
					return nil, err
				}
				return ownedFileInfo{FileInfo: info, owner: test.owner}, nil
			}}
			reason, err := policy.rejection(test.path)
			assert.NilError(t, err)
			assert.Assert(t, reason == "" && test.want == "" || test.want != "" && strings.Contains(reason, test.want))
		})
	}
}
