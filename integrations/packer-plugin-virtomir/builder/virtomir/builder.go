// Package virtomir is a Packer builder that makes VirtoMir templates: a machine from a
// cloud image, provisioned over SSH, shut down and turned into a template that VirtoMir
// deploys (linked or full clones) from then on.
package virtomir

import (
	"context"
	"errors"
	"fmt"

	"github.com/hashicorp/hcl/v2/hcldec"
	"github.com/hashicorp/packer-plugin-sdk/communicator"
	"github.com/hashicorp/packer-plugin-sdk/multistep"
	"github.com/hashicorp/packer-plugin-sdk/multistep/commonsteps"
	packersdk "github.com/hashicorp/packer-plugin-sdk/packer"
)

// Builder is the VirtoMir builder.
type Builder struct {
	config Config
	runner multistep.Runner
}

func (b *Builder) ConfigSpec() hcldec.ObjectSpec { return b.config.FlatMapstructure().HCL2Spec() }

func (b *Builder) Prepare(raws ...interface{}) ([]string, []string, error) {
	warnings, err := b.config.Prepare(raws...)
	return nil, warnings, err
}

func (b *Builder) Run(ctx context.Context, ui packersdk.Ui, hook packersdk.Hook) (packersdk.Artifact, error) {
	cl, err := newClient(&b.config)
	if err != nil {
		return nil, err
	}
	state := new(multistep.BasicStateBag)
	state.Put("config", &b.config)
	state.Put("hook", hook)
	state.Put("ui", ui)
	steps := []multistep.Step{
		&communicator.StepSSHKeyGen{CommConf: &b.config.Comm, SSHTemporaryKeyPair: b.config.Comm.SSH.SSHTemporaryKeyPair},
		&stepCreate{config: &b.config, client: cl},
		&stepWaitIP{config: &b.config, client: cl},
		&communicator.StepConnect{
			Config: &b.config.Comm,
			Host: func(st multistep.StateBag) (string, error) {
				ip, ok := st.GetOk("ip")
				if !ok {
					return "", errors.New("the machine has no address")
				}
				return ip.(string), nil
			},
			SSHConfig: b.config.Comm.SSHConfigFunc(),
		},
		new(commonsteps.StepProvision),
		&stepShutdown{config: &b.config, client: cl},
		&stepTemplate{config: &b.config, client: cl},
	}
	b.runner = commonsteps.NewRunner(steps, b.config.PackerConfig, ui)
	b.runner.Run(ctx, state)
	if err, ok := state.GetOk("error"); ok {
		return nil, err.(error)
	}
	if _, ok := state.GetOk(multistep.StateCancelled); ok {
		return nil, errors.New("build was cancelled")
	}
	if _, ok := state.GetOk(multistep.StateHalted); ok {
		return nil, errors.New("build was halted")
	}
	uuid, _ := state.Get("uuid").(string)
	return &Artifact{UUID: uuid, Name: b.config.TemplateName, Template: !b.config.KeepMachine, client: cl,
		StateData: map[string]interface{}{"generated_data": state.Get("generated_data")}}, nil
}

// Artifact is the template (or machine) a build made.
type Artifact struct {
	UUID, Name string
	Template   bool
	StateData  map[string]interface{}
	client     *client
}

func (a *Artifact) BuilderId() string { return BuilderID }
func (a *Artifact) Files() []string   { return nil }
func (a *Artifact) Id() string        { return a.UUID }

func (a *Artifact) String() string {
	kind := "machine"
	if a.Template {
		kind = "template"
	}
	return fmt.Sprintf("VirtoMir %s %s (%s)", kind, a.Name, a.UUID)
}

func (a *Artifact) State(name string) interface{} { return a.StateData[name] }

func (a *Artifact) Destroy() error {
	return a.client.remove(context.Background(), a.UUID)
}
