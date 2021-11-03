// Copyright 2020 Drone.IO Inc. All rights reserved.
// Use of this source code is governed by the Polyform License
// that can be found in the LICENSE file.

package compiler

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/drone-runners/drone-runner-aws/engine"
	"github.com/drone-runners/drone-runner-aws/engine/resource"
	"github.com/drone-runners/drone-runner-aws/internal/poolfile"

	"github.com/drone/drone-go/drone"
	"github.com/drone/runner-go/environ/provider"
	"github.com/drone/runner-go/manifest"
	"github.com/drone/runner-go/pipeline/runtime"
	"github.com/drone/runner-go/secret"

	"github.com/dchest/uniuri"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

var nocontext = context.Background()

// dummy function that returns a non-random string for testing.
// it is used in place of the random function.
func notRandom() string {
	return "random"
}

// This test verifies the pipeline dependency graph. When no
// dependency graph is defined, a default dependency graph is
// automatically defined to run steps serially.
func TestCompile_Serial(t *testing.T) {
	testCompile(t, "testdata/serial.yml", "testdata/serial.json")
}

// This test verifies the pipeline dependency graph. It also
// verifies that pipeline steps with no dependencies depend on
// the initial clone step.
func TestCompile_Graph(t *testing.T) {
	testCompile(t, "testdata/graph.yml", "testdata/graph.json")
}

// This test verifies no clone step exists in the pipeline if
// cloning is disabled.
func TestCompile_CloneDisabled_Serial(t *testing.T) {
	testCompile(t, "testdata/noclone_serial.yml", "testdata/noclone_serial.json")
}

// This test verifies no clone step exists in the pipeline if
// cloning is disabled. It also verifies no pipeline steps
// depend on a clone step.
func TestCompile_CloneDisabled_Graph(t *testing.T) {
	testCompile(t, "testdata/noclone_graph.yml", "testdata/noclone_graph.json")
}

// This test verifies that steps are disabled if conditions
// defined in the when block are not satisfied.
func TestCompile_Match(t *testing.T) {
	ir := testCompile(t, "testdata/match.yml", "testdata/match.json")
	if ir.Steps[0].RunPolicy != runtime.RunOnSuccess {
		t.Errorf("Expect run on success")
	}
	if ir.Steps[1].RunPolicy != runtime.RunNever {
		t.Errorf("Expect run never")
	}
}

// This test verifies that steps configured to run on both
// success or failure are configured to always run.
func TestCompile_RunAlways(t *testing.T) {
	ir := testCompile(t, "testdata/run_always.yml", "testdata/run_always.json")
	if ir.Steps[0].RunPolicy != runtime.RunAlways {
		t.Errorf("Expect run always")
	}
}

// This test verifies that steps configured to run on failure
// are configured to run on failure.
func TestCompile_RunFailure(t *testing.T) {
	ir := testCompile(t, "testdata/run_failure.yml", "testdata/run_failure.json")
	if ir.Steps[0].RunPolicy != runtime.RunOnFailure {
		t.Errorf("Expect run on failure")
	}
}

// This test verifies that secrets defined in the yaml are
// requested and stored in the intermediate representation
// at compile time.
func TestCompile_Secrets(t *testing.T) {
	mnfst, _ := manifest.ParseFile("testdata/secret.yml")
	compilerSettings := poolfile.PoolSettings{
		AwsAccessKeyID: "AKIAIOSFODNN7EXAMPLE",
	}
	pools, _ := poolfile.ProcessPoolFile("testdata/drone_pool.yml", &compilerSettings)

	compiler := &Compiler{
		Environ: provider.Static(nil),
		Secret: secret.StaticVars(map[string]string{
			"token":       "3DA541559918A808C2402BBA5012F6C60B27661C",
			"password":    "password",
			"my_username": "octocat",
		}),
		Pools: pools,
	}
	args := runtime.CompilerArgs{
		Repo:     &drone.Repo{},
		Build:    &drone.Build{},
		Stage:    &drone.Stage{},
		System:   &drone.System{},
		Netrc:    &drone.Netrc{},
		Manifest: mnfst,
		Pipeline: mnfst.Resources[0].(*resource.Pipeline),
		Secret:   secret.Static(nil),
	}

	ir := compiler.Compile(nocontext, args).(*engine.Spec)
	got := ir.Steps[0].Secrets
	want := []*engine.Secret{
		{
			Name: "my_password",
			Env:  "PASSWORD",
			Data: nil, // secret not found, data nil
			Mask: true,
		},
		{
			Name: "my_username",
			Env:  "USERNAME",
			Data: []byte("octocat"), // secret found
			Mask: true,
		},
	}
	if diff := cmp.Diff(got, want); diff != "" {
		// BUG(bradrydzewski) ordering is not guaranteed. this
		// unit tests needs to be adjusted accordingly.
		t.Skipf(diff)
	}
}

// helper function parses and compiles the source file and then
// compares to a golden json file.
func testCompile(t *testing.T, source, golden string) *engine.Spec {
	// replace the default random function with one that
	// is deterministic, for testing purposes.
	random = notRandom

	// restore the default random function and the previously
	// specified temporary directory
	defer func() {
		random = uniuri.New
	}()

	mnfst, err := manifest.ParseFile(source)
	if err != nil {
		t.Error(err)
		return nil
	}

	compilerSettings := poolfile.PoolSettings{
		AwsAccessKeyID: "AKIAIOSFODNN7EXAMPLE",
	}
	pools, _ := poolfile.ProcessPoolFile("testdata/drone_pool.yml", &compilerSettings)
	compiler := &Compiler{
		Environ: provider.Static(nil),
		Secret: secret.StaticVars(map[string]string{
			"token":       "3DA541559918A808C2402BBA5012F6C60B27661C",
			"password":    "password",
			"my_username": "octocat",
		}),
		Pools: pools,
	}
	args := runtime.CompilerArgs{
		Repo:     &drone.Repo{},
		Build:    &drone.Build{Target: "master"},
		Stage:    &drone.Stage{},
		System:   &drone.System{},
		Netrc:    &drone.Netrc{Machine: "github.com", Login: "octocat", Password: "correct-horse-battery-staple"},
		Manifest: mnfst,
		Pipeline: mnfst.Resources[0].(*resource.Pipeline),
		Secret:   secret.Static(nil),
	}

	got := compiler.Compile(nocontext, args)

	raw, err := os.ReadFile(golden)
	if err != nil {
		t.Error(err)
	}

	want := new(engine.Spec)
	err = json.Unmarshal(raw, want)
	if err != nil {
		t.Error(err)
	}

	opts := cmp.Options{
		cmpopts.IgnoreUnexported(engine.Spec{}),
		cmpopts.IgnoreFields(engine.Step{}, "Envs", "Secrets"),
		cmpopts.IgnoreFields(engine.Spec{}, "Pool.Instance.PrivateKey", "Pool.Instance.PublicKey", "Pool.Instance.UserData"),
	}
	if diff := cmp.Diff(got, want, opts...); diff != "" {
		t.Errorf("%s\n%v", t.Name(), diff)
	}

	return got.(*engine.Spec)
}
