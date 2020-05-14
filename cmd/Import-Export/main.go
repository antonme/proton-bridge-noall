// Copyright (c) 2020 Proton Technologies AG
//
// This file is part of ProtonMail Bridge.
//
// ProtonMail Bridge is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// ProtonMail Bridge is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with ProtonMail Bridge.  If not, see <https://www.gnu.org/licenses/>.

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"strconv"
	"strings"

	"github.com/ProtonMail/proton-bridge/internal/events"
	"github.com/ProtonMail/proton-bridge/internal/frontend"
	"github.com/ProtonMail/proton-bridge/internal/importexport"
	"github.com/ProtonMail/proton-bridge/internal/users/credentials"
	"github.com/ProtonMail/proton-bridge/pkg/args"
	"github.com/ProtonMail/proton-bridge/pkg/config"
	"github.com/ProtonMail/proton-bridge/pkg/constants"
	"github.com/ProtonMail/proton-bridge/pkg/listener"
	"github.com/ProtonMail/proton-bridge/pkg/pmapi"
	"github.com/ProtonMail/proton-bridge/pkg/updates"
	"github.com/getsentry/raven-go"
	"github.com/sirupsen/logrus"
	"github.com/urfave/cli"
)

var (
	log = logrus.WithField("pkg", "main") //nolint[gochecknoglobals]

	// How many crashes in a row.
	numberOfCrashes = 0 //nolint[gochecknoglobals]

	// After how many crashes import/export gives up starting.
	maxAllowedCrashes = 10 //nolint[gochecknoglobals]
)

func main() {
	constants.AppShortName = "importExport" //TODO

	if err := raven.SetDSN(constants.DSNSentry); err != nil {
		log.WithError(err).Errorln("Can not setup sentry DSN")
	}
	raven.SetRelease(constants.Revision)

	args.FilterProcessSerialNumberFromArgs()
	filterRestartNumberFromArgs()

	app := cli.NewApp()
	app.Name = "Protonmail Import/Export"
	app.Version = constants.BuildVersion
	app.Flags = []cli.Flag{
		cli.StringFlag{
			Name:  "log-level, l",
			Usage: "Set the log level (one of panic, fatal, error, warn, info, debug, debug-client, debug-server)"},
		cli.BoolFlag{
			Name:  "cli, c",
			Usage: "Use command line interface"},
		cli.StringFlag{
			Name:  "version-json, g",
			Usage: "Generate json version file"},
		cli.BoolFlag{
			Name:  "mem-prof, m",
			Usage: "Generate memory profile"},
		cli.BoolFlag{
			Name:  "cpu-prof, p",
			Usage: "Generate CPU profile"},
	}
	app.Usage = "ProtonMail Import/Export"
	app.Action = run

	// Always log the basic info about current import/export.
	logrus.SetLevel(logrus.InfoLevel)
	log.WithField("version", constants.Version).
		WithField("revision", constants.Revision).
		WithField("runtime", runtime.GOOS).
		WithField("build", constants.BuildTime).
		WithField("args", os.Args).
		WithField("appLong", app.Name).
		WithField("appShort", constants.AppShortName).
		Info("Run app")
	if err := app.Run(os.Args); err != nil {
		log.Error("Program exited with error: ", err)
	}
}

type panicHandler struct {
	cfg *config.Config
	err *error // Pointer to error of cli action.
}

func (ph *panicHandler) HandlePanic() {
	r := recover()
	if r == nil {
		return
	}

	config.HandlePanic(ph.cfg, fmt.Sprintf("Recover: %v", r))
	frontend.HandlePanic()

	*ph.err = cli.NewExitError("Panic and restart", 255)
	numberOfCrashes++
	log.Error("Restarting after panic")
	restartApp()
	os.Exit(255)
}

// run initializes and starts everything in a precise order.
//
// IMPORTANT: ***Read the comments before CHANGING the order ***
func run(context *cli.Context) (contextError error) { // nolint[funlen]
	// We need to have config instance to setup a logs, panic handler, etc ...
	cfg := config.New(constants.AppShortName, constants.Version, constants.Revision, "")

	// We want to know about any problem. Our PanicHandler calls sentry which is
	// not dependent on anything else. If that fails, it tries to create crash
	// report which will not be possible if no folder can be created. That's the
	// only problem we will not be notified about in any way.
	panicHandler := &panicHandler{cfg, &contextError}
	defer panicHandler.HandlePanic()

	// First we need config and create necessary folder; it's dependency for everything.
	if err := cfg.CreateDirs(); err != nil {
		log.Fatal("Cannot create necessary folders: ", err)
	}

	// Setup of logs should be as soon as possible to ensure we record every wanted report in the log.
	logLevel := context.GlobalString("log-level")
	_, _ = config.SetupLog(cfg, logLevel)

	// Doesn't make sense to continue when Import/Export was invoked with wrong arguments.
	// We should tell that to the user before we do anything else.
	if context.Args().First() != "" {
		_ = cli.ShowAppHelp(context)
		return cli.NewExitError("Unknown argument", 4)
	}

	// It's safe to get version JSON file even when other instance is running.
	// (thus we put it before check of presence of other Import/Export instance).
	updates := updates.New(
		constants.AppShortName,
		constants.Version,
		constants.Revision,
		constants.BuildTime,
		importexport.ReleaseNotes,
		importexport.ReleaseFixedBugs,
		cfg.GetUpdateDir(),
	)

	if dir := context.GlobalString("version-json"); dir != "" {
		generateVersionFiles(updates, dir)
		return nil
	}

	// In case user wants to do CPU or memory profiles...
	if doCPUProfile := context.GlobalBool("cpu-prof"); doCPUProfile {
		f, err := os.Create("cpu.pprof")
		if err != nil {
			log.Fatal("Could not create CPU profile: ", err)
		}
		if err := pprof.StartCPUProfile(f); err != nil {
			log.Fatal("Could not start CPU profile: ", err)
		}
		defer pprof.StopCPUProfile()
	}

	if doMemoryProfile := context.GlobalBool("mem-prof"); doMemoryProfile {
		defer makeMemoryProfile()
	}

	// Now we initialize all Import/Export parts.
	log.Debug("Initializing import/export...")
	eventListener := listener.New()
	events.SetupEvents(eventListener)

	credentialsStore, credentialsError := credentials.NewStore("import-export")
	if credentialsError != nil {
		log.Error("Could not get credentials store: ", credentialsError)
	}

	cm := pmapi.NewClientManager(cfg.GetAPIConfig())

	// Different build types have different roundtrippers (e.g. we want to enable
	// TLS fingerprint checks in production builds). GetRoundTripper has a different
	// implementation depending on whether build flag pmapi_prod is used or not.
	cm.SetRoundTripper(cfg.GetRoundTripper(cm, eventListener))

	importexportInstance := importexport.New(cfg, panicHandler, eventListener, cm, credentialsStore)

	// Decide about frontend mode before initializing rest of import/export.
	var frontendMode string
	switch {
	case context.GlobalBool("cli"):
		frontendMode = "cli"
	default:
		frontendMode = "qt"
	}
	log.WithField("mode", frontendMode).Debug("Determined frontend mode to use")

	frontend := frontend.NewImportExport(constants.Version, constants.BuildVersion, frontendMode, panicHandler, cfg, eventListener, updates, importexportInstance)

	// Last part is to start everything.
	log.Debug("Starting frontend...")
	if err := frontend.Loop(credentialsError); err != nil {
		log.Error("Frontend failed with error: ", err)
		return cli.NewExitError("Frontend error", 2)
	}

	if frontend.IsAppRestarting() {
		restartApp()
	}

	return nil
}

// generateVersionFiles writes a JSON file with details about current build.
// Those files are used for upgrading the app.
func generateVersionFiles(updates *updates.Updates, dir string) {
	log.Info("Generating version files")
	for _, goos := range []string{"windows", "darwin", "linux"} {
		log.Debug("Generating JSON for ", goos)
		if err := updates.CreateJSONAndSign(dir, goos); err != nil {
			log.Error(err)
		}
	}
}

func makeMemoryProfile() {
	name := "./mem.pprof"
	f, err := os.Create(name)
	if err != nil {
		log.Error("Could not create memory profile: ", err)
	}
	if abs, err := filepath.Abs(name); err == nil {
		name = abs
	}
	log.Info("Writing memory profile to ", name)
	runtime.GC() // get up-to-date statistics
	if err := pprof.WriteHeapProfile(f); err != nil {
		log.Error("Could not write memory profile: ", err)
	}
	_ = f.Close()
}

// filterRestartNumberFromArgs removes flag with a number how many restart we already did.
// See restartApp how that number is used.
func filterRestartNumberFromArgs() {
	tmp := os.Args[:0]
	for i, arg := range os.Args {
		if !strings.HasPrefix(arg, "--restart_") {
			tmp = append(tmp, arg)
			continue
		}
		var err error
		numberOfCrashes, err = strconv.Atoi(os.Args[i][10:])
		if err != nil {
			numberOfCrashes = maxAllowedCrashes
		}
	}
	os.Args = tmp
}

// restartApp starts a new instance in background.
func restartApp() {
	if numberOfCrashes >= maxAllowedCrashes {
		log.Error("Too many crashes")
		return
	}
	if exeFile, err := os.Executable(); err == nil {
		arguments := append(os.Args[1:], fmt.Sprintf("--restart_%d", numberOfCrashes))
		cmd := exec.Command(exeFile, arguments...) //nolint[gosec]
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Stdin = os.Stdin
		if err := cmd.Start(); err != nil {
			log.Error("Restart failed: ", err)
		}
	}
}