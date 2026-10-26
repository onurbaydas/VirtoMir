package virtomir

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hashicorp/packer-plugin-sdk/multistep"
	packersdk "github.com/hashicorp/packer-plugin-sdk/packer"
)

// pollEvery is how often the steps ask VirtoMir; tests shorten it.
var pollEvery = 5 * time.Second

// stepCreate makes the machine from the cloud image, with the communicator's user and key
// in its cloud-init, and starts it. It removes the machine again if the build fails.
type stepCreate struct {
	config *Config
	client *client
}

func (s *stepCreate) Run(ctx context.Context, state multistep.StateBag) multistep.StepAction {
	ui := state.Get("ui").(packersdk.Ui)
	c := s.config
	ci := map[string]any{"hostname": strings.ToLower(strings.NewReplacer(".", "-", "_", "-", "+", "-").Replace(c.TemplateName)), "user": c.Comm.SSHUsername}
	if key := strings.TrimSpace(string(c.Comm.SSHPublicKey)); key != "" {
		ci["sshKeys"] = []string{key}
	}
	if c.Comm.SSHPassword != "" {
		ci["password"] = c.Comm.SSHPassword
	}
	body := map[string]any{
		"name": c.TemplateName, "vcpus": c.VCPUs, "memoryMiB": c.MemoryMiB, "diskGiB": c.DiskGiB,
		"baseImage": c.BaseImage, "network": map[string]string{"network": c.Network}, "cloudInit": ci, "start": true,
	}
	if c.StoragePool != "" {
		body["pool"] = c.StoragePool
	}
	ui.Say(fmt.Sprintf("Creating machine %s from %s…", c.TemplateName, c.BaseImage))
	var res struct {
		UUID       string `json:"uuid"`
		StartError string `json:"startError"`
	}
	if err := s.client.do(ctx, http.MethodPost, "/api/v1/domains?wait=true", body, &res); err != nil {
		return halt(state, ui, fmt.Errorf("create the machine: %w", err))
	}
	state.Put("uuid", res.UUID)
	if res.StartError != "" {
		return halt(state, ui, fmt.Errorf("the machine was created but did not start: %s", res.StartError))
	}
	return multistep.ActionContinue
}

func (s *stepCreate) Cleanup(state multistep.StateBag) {
	uuid, ok := state.GetOk("uuid")
	_, cancelled := state.GetOk(multistep.StateCancelled)
	_, halted := state.GetOk(multistep.StateHalted)
	if !ok || (!cancelled && !halted) {
		return
	}
	ui := state.Get("ui").(packersdk.Ui)
	ui.Say("Removing the machine…")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := s.client.remove(ctx, uuid.(string)); err != nil {
		ui.Error(fmt.Sprintf("The machine %s was not removed: %s", uuid, err))
	}
}

// stepWaitIP waits until VirtoMir sees the machine's address.
type stepWaitIP struct {
	config *Config
	client *client
}

func (s *stepWaitIP) Run(ctx context.Context, state multistep.StateBag) multistep.StepAction {
	ui := state.Get("ui").(packersdk.Ui)
	uuid := state.Get("uuid").(string)
	ui.Say("Waiting for the machine's address…")
	deadline := time.Now().Add(s.config.IPWaitTimeout)
	for {
		m, err := s.client.machine(ctx, uuid)
		if err == nil {
			if ip := m.ipv4(); ip != "" {
				ui.Say("Its address is " + ip)
				state.Put("ip", ip)
				return multistep.ActionContinue
			}
		}
		if time.Now().After(deadline) {
			return halt(state, ui, fmt.Errorf("the machine had no address after %s", s.config.IPWaitTimeout))
		}
		select {
		case <-ctx.Done():
			return halt(state, ui, ctx.Err())
		case <-time.After(pollEvery):
		}
	}
}

func (s *stepWaitIP) Cleanup(multistep.StateBag) {}

// stepShutdown shuts the machine down after provisioning, switching it off if it takes too long.
type stepShutdown struct {
	config *Config
	client *client
}

func (s *stepShutdown) Run(ctx context.Context, state multistep.StateBag) multistep.StepAction {
	ui := state.Get("ui").(packersdk.Ui)
	uuid := state.Get("uuid").(string)
	ui.Say("Shutting the machine down…")
	if err := s.client.action(ctx, uuid, "shutdown"); err != nil {
		return halt(state, ui, err)
	}
	deadline := time.Now().Add(s.config.ShutdownTimeout)
	for {
		m, err := s.client.machine(ctx, uuid)
		if err == nil && m.State == "shutoff" {
			return multistep.ActionContinue
		}
		if time.Now().After(deadline) {
			ui.Say("It did not shut down in time; switching it off.")
			if err := s.client.action(ctx, uuid, "force-off"); err != nil {
				return halt(state, ui, err)
			}
			return multistep.ActionContinue
		}
		select {
		case <-ctx.Done():
			return halt(state, ui, ctx.Err())
		case <-time.After(pollEvery):
		}
	}
}

func (s *stepShutdown) Cleanup(multistep.StateBag) {}

// stepTemplate turns the machine into a template.
type stepTemplate struct {
	config *Config
	client *client
}

func (s *stepTemplate) Run(ctx context.Context, state multistep.StateBag) multistep.StepAction {
	if s.config.KeepMachine {
		return multistep.ActionContinue
	}
	ui := state.Get("ui").(packersdk.Ui)
	uuid := state.Get("uuid").(string)
	ui.Say("Turning the machine into a template…")
	if err := s.client.do(ctx, http.MethodPut, "/api/v1/domains/"+url.PathEscape(uuid)+"/template", map[string]bool{"template": true}, nil); err != nil {
		return halt(state, ui, err)
	}
	return multistep.ActionContinue
}

func (s *stepTemplate) Cleanup(multistep.StateBag) {}

func halt(state multistep.StateBag, ui packersdk.Ui, err error) multistep.StepAction {
	state.Put("error", err)
	ui.Error(err.Error())
	return multistep.ActionHalt
}
