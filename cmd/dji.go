package cmd

import (
	"errors"
	"fmt"
	"io"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/jborkowski/vmc/internal/djimic"
)

// errReported marks a dji failure already printed to stderr; main exits 1
// without logging it again.
var errReported = errors.New("dji: already reported")

// IsReported reports whether err was already shown to the user.
func IsReported(err error) bool {
	return errors.Is(err, errReported)
}

// Test seams: the real system shells out to diskutil/ioreg/launchctl.
var (
	djiSystem     = func() djimic.System { return djimic.ExecSystem{} }
	djiVolumesDir = "/Volumes"
)

var (
	djiDeleteAfter, djiEjectAfter *bool
	djiDryRun, djiIfPresent       bool
	djiQuiet                      bool
	djiSource, djiDest, djiStamp  string
)

// boolOverride sets *dst to val when its flag is given, so opposing flags
// like --keep/--delete resolve last-wins in command-line order.
type boolOverride struct {
	dst **bool
	val bool
}

func (b boolOverride) String() string { return "false" }
func (b boolOverride) Type() string   { return "bool" }
func (b boolOverride) Set(s string) error {
	on, err := strconv.ParseBool(s)
	if err != nil {
		return err
	}
	if on {
		v := b.val
		*b.dst = &v
	}
	return nil
}

func djiLogger(out io.Writer, quiet bool) (*djimic.FileLogger, error) {
	path, err := cfg.DJI.LogFilePath()
	if err != nil {
		return nil, err
	}
	return &djimic.FileLogger{Path: path, Quiet: quiet, Stdout: out}, nil
}

func djiTool(cmd *cobra.Command, quiet bool) (*djimic.Tool, error) {
	l, err := djiLogger(cmd.OutOrStdout(), quiet)
	if err != nil {
		return nil, err
	}
	t := djimic.New(cfg.DJI, l.Logf)
	t.Sys = djiSystem()
	t.VolumesDir = djiVolumesDir
	t.Out = cmd.OutOrStdout()
	return t, nil
}

// djiReport prints err as "error: ..." (like the dji-mic script) and returns
// errReported so neither cobra nor main prints it again.
func djiReport(cmd *cobra.Command, err error) error {
	if err == nil || IsReported(err) {
		return err
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "error: %v\n", err)
	return errReported
}

var djiCmd = &cobra.Command{
	Use:   "dji",
	Short: "Pull recordings off a DJI Wireless Mic and watch for it on mount",
	Long: `Match the transmitter by [dji] media_name, volume_uuid, or usb_vendor +
usb_product with audio_dir_glob folders. Set these only in
~/.config/vmc/config.toml.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) > 0 {
			return djiReport(cmd, fmt.Errorf("unknown command: %s", args[0]))
		}
		cmd.SetOut(cmd.ErrOrStderr())
		_ = cmd.Help()
		return errReported
	},
}

var djiPullCmd = &cobra.Command{
	Use:   "pull",
	Short: "Copy audio from the mounted transmitter into <root>/<stamp>/",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		tool, err := djiTool(cmd, djiQuiet)
		if err != nil {
			return djiReport(cmd, err)
		}
		opts := djimic.DefaultPullOptions(cfg.DJI)
		if djiDeleteAfter != nil {
			opts.DeleteAfter = *djiDeleteAfter
		}
		if djiEjectAfter != nil {
			opts.EjectAfter = *djiEjectAfter
		}
		opts.DryRun = djiDryRun
		opts.IfPresent = djiIfPresent
		opts.Source = djiSource
		opts.Dest = djiDest
		opts.Stamp = djiStamp
		_, err = tool.Pull(opts)
		return djiReport(cmd, err)
	},
}

var djiDetectCmd = &cobra.Command{
	Use:   "detect",
	Short: "Print the matched volume and fingerprints (exit 1 if absent)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		tool, err := djiTool(cmd, false)
		if err != nil {
			return djiReport(cmd, err)
		}
		if err := tool.Detect(); errors.Is(err, djimic.ErrNotMounted) {
			return errReported
		} else if err != nil {
			return djiReport(cmd, err)
		}
		return nil
	},
}

var djiEjectCmd = &cobra.Command{
	Use:   "eject",
	Short: "Safely eject the transmitter so it can be unplugged",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		tool, err := djiTool(cmd, false)
		if err != nil {
			return djiReport(cmd, err)
		}
		return djiReport(cmd, tool.Eject())
	},
}

var djiWatchCmd = &cobra.Command{
	Use:       "watch enable|disable|status",
	Short:     "Manage the launchd WatchPaths /Volumes watcher",
	ValidArgs: []string{"enable", "disable", "status"},
	RunE: func(cmd *cobra.Command, args []string) error {
		action := ""
		if len(args) > 0 {
			action = args[0]
		}
		if action != "enable" && action != "disable" && action != "status" {
			return djiReport(cmd, errors.New("watch needs enable, disable, or status"))
		}
		out := cmd.OutOrStdout()
		l, err := djiLogger(out, false)
		if err != nil {
			return djiReport(cmd, err)
		}
		w, err := djimic.NewWatch(l.Path)
		if err != nil {
			return djiReport(cmd, err)
		}
		w.Sys = djiSystem()
		switch action {
		case "enable":
			if err := w.Enable(); err != nil {
				return djiReport(cmd, err)
			}
			l.Logf("watch enabled: WatchPaths /Volumes → %s", w.PlistPath)
			fmt.Fprintln(out, "Watcher is armed. It runs when /Volumes changes (plug/unplug).")
		case "disable":
			w.Disable()
			l.Logf("watch disabled")
		case "status":
			if loaded, lines := w.Status(); loaded {
				fmt.Fprintln(out, "watch: enabled")
				for _, line := range lines {
					fmt.Fprintln(out, line)
				}
			} else {
				fmt.Fprintln(out, "watch: disabled")
			}
			fmt.Fprintf(out, "plist: %s\n", w.PlistPath)
			fmt.Fprintf(out, "log:   %s\n", w.LogFile)
		}
		return nil
	},
}

func init() {
	f := djiPullCmd.Flags()
	f.Var(boolOverride{&djiDeleteAfter, false}, "keep", "keep files on the device (copy only)")
	f.Var(boolOverride{&djiDeleteAfter, true}, "delete", "delete from the device after a verified copy (default from dji.delete_after)")
	f.Var(boolOverride{&djiEjectAfter, true}, "eject", "after a successful copy, eject so it is safe to unplug (default from dji.eject_after)")
	f.Var(boolOverride{&djiEjectAfter, false}, "no-eject", "leave the volume mounted even after a successful copy")
	for _, name := range []string{"keep", "delete", "eject", "no-eject"} {
		f.Lookup(name).NoOptDefVal = "true"
	}
	f.BoolVar(&djiDryRun, "dry-run", false, "print actions without copying or deleting")
	f.BoolVar(&djiIfPresent, "if-present", false, "exit 0 when the device is not mounted (for the watcher)")
	f.StringVar(&djiSource, "source", "", "use this volume instead of auto-detect")
	f.StringVar(&djiDest, "dest", "", "destination root (default dji.root)")
	f.StringVar(&djiStamp, "stamp", "", "folder name instead of the current dji.date_fmt time")
	f.BoolVar(&djiQuiet, "quiet", false, "less stdout (still logs)")

	djiCmd.SetFlagErrorFunc(func(c *cobra.Command, err error) error {
		return djiReport(c, err)
	})
	djiCmd.AddCommand(djiPullCmd, djiDetectCmd, djiEjectCmd, djiWatchCmd)
	for _, c := range append(djiCmd.Commands(), djiCmd) {
		c.SilenceErrors = true
		c.SilenceUsage = true
	}
	rootCmd.AddCommand(djiCmd)
}
