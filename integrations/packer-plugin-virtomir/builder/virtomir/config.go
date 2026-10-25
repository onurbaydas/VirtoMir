//go:generate packer-sdc mapstructure-to-hcl2 -type Config

package virtomir

import (
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/hashicorp/packer-plugin-sdk/common"
	"github.com/hashicorp/packer-plugin-sdk/communicator"
	packersdk "github.com/hashicorp/packer-plugin-sdk/packer"
	"github.com/hashicorp/packer-plugin-sdk/template/config"
	"github.com/hashicorp/packer-plugin-sdk/template/interpolate"
)

// BuilderID identifies the artifacts of this builder.
const BuilderID = "virtomir.builder"

// Config is the builder's configuration: a machine made from a cloud image on a VirtoMir
// host, provisioned over SSH and turned into a template.
type Config struct {
	common.PackerConfig `mapstructure:",squash"`
	Comm                communicator.Config `mapstructure:",squash"`

	// URL of VirtoMir, e.g. https://hv1:8420 (or VIRTOMIR_URL).
	URL string `mapstructure:"url" required:"true"`
	// Token is a VirtoMir API token of an admin (or VIRTOMIR_TOKEN).
	Token string `mapstructure:"token" required:"true"`
	// Insecure accepts the host's self-signed certificate.
	Insecure bool `mapstructure:"insecure"`
	// Host is the cluster member to build on (default: the one the URL names).
	Host string `mapstructure:"host"`

	// TemplateName is the name of the template the build makes.
	TemplateName string `mapstructure:"template_name" required:"true"`
	// BaseImage is the path of a cloud image on the host (Debian, Ubuntu, Fedora, …).
	BaseImage   string `mapstructure:"base_image" required:"true"`
	StoragePool string `mapstructure:"storage_pool"`
	DiskGiB     uint64 `mapstructure:"disk_gib"`
	VCPUs       int    `mapstructure:"vcpus"`
	MemoryMiB   uint64 `mapstructure:"memory_mib"`
	Network     string `mapstructure:"network"`
	// KeepMachine leaves a plain machine instead of a template.
	KeepMachine bool `mapstructure:"keep_machine"`
	// IPWaitTimeout bounds the wait for the machine's address (default 10m).
	IPWaitTimeout time.Duration `mapstructure:"ip_wait_timeout"`
	// ShutdownTimeout is how long it may take to shut down before it is switched off (default 5m).
	ShutdownTimeout time.Duration `mapstructure:"shutdown_timeout"`

	ctx interpolate.Context
}

var nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$`)

// Prepare decodes and checks the configuration.
func (c *Config) Prepare(raws ...interface{}) ([]string, error) {
	err := config.Decode(c, &config.DecodeOpts{
		PluginType:         BuilderID,
		Interpolate:        true,
		InterpolateContext: &c.ctx,
	}, raws...)
	if err != nil {
		return nil, err
	}
	if c.URL == "" {
		c.URL = envOr("VIRTOMIR_URL")
	}
	if c.Token == "" {
		c.Token = envOr("VIRTOMIR_TOKEN")
	}
	if !c.Insecure && envOr("VIRTOMIR_INSECURE") == "true" {
		c.Insecure = true
	}
	if c.VCPUs == 0 {
		c.VCPUs = 2
	}
	if c.MemoryMiB == 0 {
		c.MemoryMiB = 2048
	}
	if c.DiskGiB == 0 {
		c.DiskGiB = 20
	}
	if c.Network == "" {
		c.Network = "default"
	}
	if c.IPWaitTimeout == 0 {
		c.IPWaitTimeout = 10 * time.Minute
	}
	if c.ShutdownTimeout == 0 {
		c.ShutdownTimeout = 5 * time.Minute
	}
	if c.Comm.Type == "" {
		c.Comm.Type = "ssh"
	}
	if c.Comm.SSHUsername == "" {
		c.Comm.SSHUsername = "packer"
	}

	var errs *packersdk.MultiError
	if es := c.Comm.Prepare(&c.ctx); len(es) > 0 {
		errs = packersdk.MultiErrorAppend(errs, es...)
	}
	if c.URL == "" {
		errs = packersdk.MultiErrorAppend(errs, errors.New("url is required (or VIRTOMIR_URL)"))
	}
	if c.Token == "" {
		errs = packersdk.MultiErrorAppend(errs, errors.New("token is required (or VIRTOMIR_TOKEN): an API token of an admin"))
	}
	if !nameRE.MatchString(c.TemplateName) {
		errs = packersdk.MultiErrorAppend(errs, fmt.Errorf("template_name %q is not a machine name: 1-64 letters, digits, '.', '_', '+' or '-'", c.TemplateName))
	}
	if c.BaseImage == "" {
		errs = packersdk.MultiErrorAppend(errs, errors.New("base_image is required: the path of a cloud image on the host"))
	}
	if errs != nil && len(errs.Errors) > 0 {
		return nil, errs
	}
	return nil, nil
}
