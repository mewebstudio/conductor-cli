package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/mewebstudio/conductor-cli/internal/config"
	"github.com/mewebstudio/conductor-cli/internal/providers"
	"github.com/mewebstudio/conductor-cli/internal/secrets"
	"github.com/mewebstudio/conductor-cli/internal/ui"
)

func providersCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "providers",
		Short: "List, enable or disable non-Claude providers (Codex, NVIDIA, plugins)",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			c, err := config.Load()
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(w, "PROVIDER\tENABLED\tCAN\tSTATE\tDESCRIPTION")
			for _, p := range providers.All() {
				state := "ready"
				if err := p.Check(); err != nil {
					state = err.Error()
				}
				fmt.Fprintf(w, "%s\t%v\t%s\t%s\t%s\n", p.Name(), c.ProviderEnabled(p.Name()),
					strings.Join(p.Capabilities(), ","), state, p.Description())
			}
			if err := w.Flush(); err != nil {
				return err
			}
			for dir, err := range providers.PluginProblems() {
				ui.Warnf("plugin %s: %v", tilde(dir), err)
			}
			return nil
		},
	}

	enable := editFlags(&cobra.Command{
		Use:   "enable <provider>",
		Short: "Turn a provider on (asks for its API key if it needs one)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := enableProvider(cmd.Context(), args[0], isLocal(cmd)); err != nil {
				return err
			}
			return changed(cmd)
		},
	})

	disable := editFlags(&cobra.Command{
		Use:   "disable <provider>",
		Short: "Turn a provider off and take it out of the review chain",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := providers.Get(args[0])
			if err != nil {
				return err
			}
			c, err := config.Load()
			if err != nil {
				return err
			}
			if err := config.SetValue("providers.enabled", without(c.Providers.Enabled, p.Name()), isLocal(cmd)); err != nil {
				return err
			}
			if err := config.SetValue("review.chain", without(c.Review.Chain, p.Name()), isLocal(cmd)); err != nil {
				return err
			}
			if p.NeedsKey() && secrets.Has(p.Name()) {
				del, _ := cmd.Flags().GetBool("delete-key")
				if !del && ui.Interactive() {
					del, _ = ui.Confirm("Also delete the stored API key on this machine?", "", false)
				}
				if del {
					if err := secrets.Delete(p.Name()); err != nil {
						return err
					}
					ui.Okf("API key deleted from the credential store")
				}
			}
			return changed(cmd)
		},
	})
	disable.Flags().Bool("delete-key", false, "also delete the stored API key")

	key := &cobra.Command{
		Use:   "key <provider>",
		Short: "Store or replace a provider's API key in the OS credential store",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := providers.Get(args[0])
			if err != nil {
				return err
			}
			if !p.NeedsKey() {
				return fmt.Errorf("%s does not use an API key", p.Name())
			}
			_, err = askKey(cmd.Context(), p.Name(), true)
			return err
		},
	}
	cmd.AddCommand(enable, disable, key)
	return cmd
}

func without(list []string, name string) []string {
	out := []string{}
	for _, x := range list {
		if x != name {
			out = append(out, x)
		}
	}
	return out
}

// enableProvider switches a provider on, collects its key (and, for NVIDIA,
// a model), and puts it into the review chain before the Claude reviewer.
func enableProvider(ctx context.Context, name string, local bool) error {
	p, err := providers.Get(name)
	if err != nil {
		return err
	}
	c, err := config.Load()
	if err != nil {
		return err
	}
	var models []string
	if p.NeedsKey() {
		models, err = askKey(ctx, p.Name(), false)
		if err != nil {
			return err
		}
	}
	switch p.Name() {
	case "nvidia":
		if err := chooseNVIDIAModel(models, c.Reviewer("nvidia").Model, local); err != nil {
			return err
		}
	case "antigravity":
		if err := chooseListedModel(p.Name(), c.Reviewer(p.Name()).Model, local); err != nil {
			return err
		}
	}
	if !c.ProviderEnabled(p.Name()) {
		if err := config.SetValue("providers.enabled", append(c.Providers.Enabled, p.Name()), local); err != nil {
			return err
		}
	}
	if providers.Has(p, "review") && !slices.Contains(c.Review.Chain, p.Name()) {
		chain := without(c.Review.Chain, "claude")
		chain = append(chain, p.Name())
		if slices.Contains(c.Review.Chain, "claude") {
			chain = append(chain, "claude")
		}
		if err := config.SetValue("review.chain", chain, local); err != nil {
			return err
		}
	}
	ui.Okf("%s enabled", p.Name())
	return nil
}

// askKey reuses a stored key when it still works, otherwise prompts for a new
// one (hidden), validates it and stores it. For NVIDIA it returns the models
// the key can reach.
func askKey(ctx context.Context, name string, replace bool) ([]string, error) {
	validate := func(k string) ([]string, error) {
		if name == "nvidia" {
			return providers.NVIDIAModels(ctx, k)
		}
		return nil, nil
	}
	if !replace {
		if k, err := secrets.Get(name); err == nil {
			if models, err := validate(k); err == nil {
				ui.Okf("using the %s API key already stored on this machine", name)
				return models, nil
			}
			ui.Warnf("the stored %s key no longer works; enter a new one", name)
		}
	}
	if !ui.Interactive() {
		return nil, fmt.Errorf("%s needs an API key: run `conductor providers key %s` in a terminal", name, name)
	}
	hint := "Stored only in this machine's credential store, never in files or the config repo."
	if name == "nvidia" {
		hint = "Create one at build.nvidia.com. " + hint
	}
	for attempt := 0; attempt < 3; attempt++ {
		k, err := ui.Secret(name+" API key", hint)
		if err != nil {
			return nil, err
		}
		if k == "" {
			return nil, errors.New("no key entered")
		}
		models, err := validate(k)
		if errors.Is(err, providers.ErrBadKey) {
			ui.Failf("the key was rejected; try again")
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("could not validate the key: %w", err)
		}
		if err := secrets.Set(name, k); err != nil {
			return nil, fmt.Errorf("storing the key: %w", err)
		}
		ui.Okf("%s API key verified and stored", name)
		return models, nil
	}
	return nil, errors.New("key rejected three times; nothing stored")
}

func chooseNVIDIAModel(models []string, current string, local bool) error {
	if len(models) == 0 {
		return nil
	}
	def := current
	if !slices.Contains(models, def) {
		def = providers.SuggestNVIDIAModel(models)
	}
	pick := def
	if ui.Interactive() {
		opts := make([]ui.Option, len(models))
		for i, m := range models {
			opts[i] = ui.Option{Label: m, Value: m}
		}
		var err error
		pick, err = ui.Select("NVIDIA model", fmt.Sprintf("%d models available to your key; type to filter.", len(models)), opts, def)
		if err != nil {
			return err
		}
	}
	return config.SetValue("reviewers.nvidia.model", pick, local)
}

// chooseListedModel lets you pick a reviewer model from the tool's own list.
func chooseListedModel(name, current string, local bool) error {
	list := availableFor(name)
	if len(list) == 0 || !ui.Interactive() {
		return nil // keep the default; `conductor review set` can change it later
	}
	opts := make([]ui.Option, len(list))
	for i, m := range list {
		opts[i] = ui.Option{Label: fmt.Sprintf("%-26s %s", m.ID, m.Label), Value: m.ID}
	}
	def := current
	if def == "" {
		def = list[0].ID
	}
	pick, err := ui.Select(name+" model", "Models your "+name+" account offers.", opts, def)
	if err != nil {
		return err
	}
	return config.SetValue("reviewers."+name+".model", pick, local)
}
