// pushcli is a simple command-line application for pushing a package to head.
package main

import (
	"flag"
	"fmt"
	"net/http"
	"strings"

	"github.com/skia-dev/glog"
	"go.skia.org/infra/go/auth"
	"go.skia.org/infra/go/common"
	"go.skia.org/infra/go/packages"
	"go.skia.org/infra/go/util"
	"google.golang.org/api/compute/v1"
	"google.golang.org/api/storage/v1"
)

var (
	project        = flag.String("project", "google.com:skia-buildbots", "The Google Compute Engine project.")
	rollback       = flag.Bool("rollback", false, "If true roll back to the next most recent package, otherwise use the most recently pushed package.")
	force          = flag.Bool("force", false, "If true then install the package even if it hasn't previously been installed on the given server.")
	dryrun         = flag.Bool("dryrun", false, "If true don't actually push, but just log what actions would be taken.")
	configFilename = flag.String("config_filename", "skiapush.conf", "Config filename used by Push.")
)

func init() {
	flag.Usage = func() {
		fmt.Printf(`Usage: pushcli [options] <package> <server>

Pushes the latest version of <package> to <server>.

  <package> - The name of the package, e.g. "pulld"
  <server> - The name of the server, e.g. "skia-monitoring".  Can be * to affect all servers that currently have the package installed.

Use the --rollback flag to force a rollback to the previous version. Note that this always picks
the next most recent package, regardless of the version of the package currently deployed.

`)
		flag.PrintDefaults()
	}
}

func main() {
	defer common.LogPanic()
	common.Init()

	// Parse out the non-flag arguments.
	args := flag.Args()
	if len(args) != 2 {
		glog.Errorf("Requires two arguments.  Saw %q\n", args)
		flag.Usage()
		return
	}
	appName := args[0]    // "skdebuggerd"
	serverName := args[1] // "skia-debugger" or "*"

	// Create the needed clients.
	client, err := auth.NewDefaultJWTServiceAccountClient(storage.DevstorageReadWriteScope, compute.ComputeReadonlyScope)
	if err != nil {
		glog.Fatalf("Failed to create authenticated HTTP client: %s\nDid you run get_service_account?", err)
	}
	store, err := storage.New(client)
	if err != nil {
		glog.Fatalf("Failed to create storage service client: %s", err)
	}
	comp, err := compute.New(client)
	if err != nil {
		glog.Fatalf("Failed to create compute service client: %s", err)
	}

	servers, err := expand(appName, serverName)
	if err != nil {
		glog.Fatalf("Failed to enumerate servers: %s", err)
	}

	glog.Infof("Installing %s to servers %q", appName, servers)

	for _, s := range servers {
		if err := installOnServer(client, store, comp, appName, s); err != nil {
			glog.Fatalf(err.Error())
		}
	}
}

// expand returns a slice of the server names that should be affected.  If 's' is "*", it will look up all
// instances in the project and return the list of instance names that have appName installed.
func expand(appName, s string) ([]string, error) {
	if s != "*" {
		return []string{s}, nil
	}
	config, err := packages.LoadPackageConfig(*configFilename)
	if err != nil {
		return nil, fmt.Errorf("Failed to load PackageConfig file %s: %s", *configFilename, err)
	}

	return config.AllServerNamesWithPackage(appName), nil
}

// installOnServer installs the named app on the compute engine instance of the given name.  It then tries to force
// pulld to pick up the changes by pinging the ip address of the server directly.
func installOnServer(client *http.Client, store *storage.Service, comp *compute.Service, appName, serverName string) error {
	// Get the current set of packages installed on the server.
	installed, err := packages.InstalledForServer(client, store, serverName)
	if err != nil {
		return fmt.Errorf("Failed to get the current installed packages on %s: %s", serverName, err)
	}
	glog.Infof("Installed Packages on %s:\n%s", serverName, strings.Join(installed.Names, "\n"))

	// Get the sorted list of available versions of the given package.
	available, err := packages.AllAvailableApp(store, appName)
	if err != nil {
		return fmt.Errorf("Failed to get the list of available versions for package %s: %s", appName, err)
	}
	glog.Infof("Available: %s", packages.PackageSlice(available).String())

	// By default roll to head, which is the first entry in the slice.
	latest := available[0]
	if *rollback {
		if len(available) == 1 {
			return fmt.Errorf("Can't rollback a package with only one version.")
		}
		latest = available[1]
	}

	found := false
	// Build a new list of packages that is the old list of packages with the new package added.
	newInstalled := []string{fmt.Sprintf("%s/%s", appName, latest.Name)}
	for _, name := range installed.Names {
		if strings.Split(name, "/")[0] == appName {
			found = true
			continue
		}
		newInstalled = append(newInstalled, name)
	}
	if !found && !*force {
		return fmt.Errorf("The application %s isn't currently installed on server %s. (Use --force to override.)", appName, serverName)
	}

	if *dryrun {
		glog.Info("Is in dry run mode.  Would be calling")
		glog.Infof(`packages.PutInstalled(store, "%s", %q, %d)`, serverName, newInstalled, installed.Generation)
	} else {
		// Write the new list of packages back to Google Storage.
		if err := packages.PutInstalled(store, serverName, newInstalled, installed.Generation); err != nil {
			return fmt.Errorf("Failed to write updated package for %s: %s", appName, err)
		}
	}

	// If we are on the right network we can ping pulld to install the new
	// package and avoid the 15s wait for pulld to poll and find the new package.
	if ip, err := findIPAddress(comp, serverName); err == nil {
		if *dryrun {
			glog.Infof(`"client.Get(http://%s:10114/pullpullpull)"`, ip)
		} else {
			glog.Infof("findIPAddress: %q", ip)
			resp, err := client.Get(fmt.Sprintf("http://%s:10114/pullpullpull", ip))
			if err != nil || resp == nil {
				glog.Infof("Failed to trigger an instant pull for server %s: %v %v", serverName, err)
			} else {
				util.Close(resp.Body)
			}
		}
	} else {
		glog.Warningf("Could not find ip address: %s", err)
	}

	return nil
}

// findIPAddress returns the ip address of the server with the given name.
func findIPAddress(comp *compute.Service, name string) (string, error) {
	// We have to look in each zone for the server with the given name.
	zones, err := comp.Zones.List(*project).Do()
	if err != nil {
		return "", fmt.Errorf("Failed to list zones: %s", err)
	}
	for _, zone := range zones.Items {
		item, err := comp.Instances.Get(*project, zone.Name, name).Do()
		if err != nil {
			continue
		}
		for _, nif := range item.NetworkInterfaces {
			for _, acc := range nif.AccessConfigs {
				if strings.HasPrefix(strings.ToLower(acc.Name), "external") {
					return acc.NatIP, nil
				}
			}
		}
	}
	return "", fmt.Errorf("Couldn't find an instance named: %s", name)
}
