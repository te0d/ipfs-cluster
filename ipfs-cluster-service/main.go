package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"syscall"

	"context"

	//	_ "net/http/pprof"

	logging "github.com/ipfs/go-log"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/urfave/cli"
	libp2praft "github.com/libp2p/go-libp2p-raft"
	host "github.com/libp2p/go-libp2p-host"
	basichost "github.com/libp2p/go-libp2p/p2p/host/basic"
	peerstore "github.com/libp2p/go-libp2p-peerstore"
	swarm "github.com/libp2p/go-libp2p-swarm"
	hraft "github.com/hashicorp/raft"
	msgpack "github.com/multiformats/go-multicodec/msgpack"

	ipfscluster "github.com/ipfs/ipfs-cluster"
	"github.com/ipfs/ipfs-cluster/allocator/ascendalloc"
	"github.com/ipfs/ipfs-cluster/allocator/descendalloc"
	"github.com/ipfs/ipfs-cluster/api/rest"
	"github.com/ipfs/ipfs-cluster/config"
	"github.com/ipfs/ipfs-cluster/consensus/raft"
	"github.com/ipfs/ipfs-cluster/informer/disk"
	"github.com/ipfs/ipfs-cluster/informer/numpin"
	"github.com/ipfs/ipfs-cluster/ipfsconn/ipfshttp"
	"github.com/ipfs/ipfs-cluster/monitor/basic"
	"github.com/ipfs/ipfs-cluster/pintracker/maptracker"
	"github.com/ipfs/ipfs-cluster/state/mapstate"
)

// ProgramName of this application
const programName = `ipfs-cluster-service`

// We store a commit id here
var commit string

// Description provides a short summary of the functionality of this tool
var Description = fmt.Sprintf(`
%s runs an IPFS Cluster node.

A node participates in the cluster consensus, follows a distributed log
of pinning and unpinning requests and manages pinning operations to a
configured IPFS daemon.

This node also provides an API for cluster management, an IPFS Proxy API which
forwards requests to IPFS and a number of components for internal communication
using LibP2P. This is a simplified view of the components:

             +------------------+
             | ipfs-cluster-ctl |
             +---------+--------+
                       |
                       | HTTP
ipfs-cluster-service   |                           HTTP
+----------+--------+--v--+----------------------+      +-------------+
| RPC/Raft | Peer 1 | API | IPFS Connector/Proxy +------> IPFS daemon |
+----^-----+--------+-----+----------------------+      +-------------+
     | libp2p
     |
+----v-----+--------+-----+----------------------+      +-------------+
| RPC/Raft | Peer 2 | API | IPFS Connector/Proxy +------> IPFS daemon |
+----^-----+--------+-----+----------------------+      +-------------+
     |
     |
+----v-----+--------+-----+----------------------+      +-------------+
| RPC/Raft | Peer 3 | API | IPFS Connector/Proxy +------> IPFS daemon |
+----------+--------+-----+----------------------+      +-------------+


%s needs a valid configuration to run. This configuration is
independent from IPFS and includes its own LibP2P key-pair. It can be
initialized with "init" and its default location is
 ~/%s/%s.

For feedback, bug reports or any additional information, visit
https://github.com/ipfs/ipfs-cluster.


EXAMPLES

Initial configuration:

$ ipfs-cluster-service init

Launch a cluster:

$ ipfs-cluster-service

Launch a peer and join existing cluster:

$ ipfs-cluster-service --bootstrap /ip4/192.168.1.2/tcp/9096/ipfs/QmPSoSaPXpyunaBwHs1rZBKYSqRV4bLRk32VGYLuvdrypL
`,
	programName,
	programName,
	DefaultPath,
	DefaultConfigFile)

var logger = logging.Logger("service")

// Default location for the configurations and data
var (
	// DefaultPath is initialized to something like ~/.ipfs-cluster/service.json
	// and holds all the ipfs-cluster data
	DefaultPath = ".ipfs-cluster"
	// The name of the configuration file inside DefaultPath
	DefaultConfigFile = "service.json"
)

var (
	configPath string
)


func init() {
	// Set the right commit. The only way I could make this work
	ipfscluster.Commit = commit

	usr, err := user.Current()
	if err != nil {
		panic("cannot guess the current user")
	}
	DefaultPath = filepath.Join(
		usr.HomeDir,
		".ipfs-cluster")
}

func out(m string, a ...interface{}) {
	fmt.Fprintf(os.Stderr, m, a...)
}

func checkErr(doing string, err error) {
	if err != nil {
		out("error %s: %s\n", doing, err)
		os.Exit(1)
	}
}

func main() {
	// go func() {
	//	log.Println(http.ListenAndServe("localhost:6060", nil))
	// }()

	app := cli.NewApp()
	app.Name = programName
	app.Usage = "IPFS Cluster node"
	app.Description = Description
	//app.Copyright = "© Protocol Labs, Inc."
	app.Version = ipfscluster.Version
	app.Flags = []cli.Flag{
		cli.StringFlag{
			Name:   "config, c",
			Value:  DefaultPath,
			Usage:  "path to the configuration and data `FOLDER`",
			EnvVar: "IPFS_CLUSTER_PATH",
		},
		cli.BoolFlag{
			Name:  "force, f",
			Usage: "forcefully proceed with some actions. i.e. overwriting configuration",
		},
		cli.StringFlag{
			Name:  "bootstrap, j",
			Usage: "join a cluster providing an existing peer's `multiaddress`. Overrides the \"bootstrap\" values from the configuration",
		},
		cli.BoolFlag{
			Name:   "leave, x",
			Usage:  "remove peer from cluster on exit. Overrides \"leave_on_shutdown\"",
			Hidden: true,
		},
		cli.BoolFlag{
			Name:  "debug, d",
			Usage: "enable full debug logging (very verbose)",
		},
		cli.StringFlag{
			Name:  "loglevel, l",
			Value: "info",
			Usage: "set the loglevel for cluster only [critical, error, warning, info, debug]",
		},
		cli.StringFlag{
			Name:  "alloc, a",
			Value: "disk-freespace",
			Usage: "allocation strategy to use [disk-freespace,disk-reposize,numpin].",
		},
	}

	app.Commands = []cli.Command{
		{
			Name:  "init",
			Usage: "create a default configuration and exit",
			Description: fmt.Sprintf(`
This command will initialize a new service.json configuration file
for %s.

By default, %s requires a cluster secret. This secret will be
automatically generated, but can be manually provided with --custom-secret
(in which case it will be prompted), or by setting the CLUSTER_SECRET
environment variable.

The private key for the libp2p node is randomly generated in all cases.

Note that the --force first-level-flag allows to overwrite an existing
configuration.
`, programName, programName),
			ArgsUsage: " ",
			Flags: []cli.Flag{
				cli.BoolFlag{
					Name:  "custom-secret, s",
					Usage: "prompt for the cluster secret",
				},
			},
			Action: func(c *cli.Context) error {
				userSecret, userSecretDefined := userProvidedSecret(c.Bool("custom-secret"))
				cfg, clustercfg, _, _, _, _, _, _ := makeConfigs()

				// Generate defaults for all registered components
				err := cfg.Default()
				checkErr("generating default configuration", err)

				// Set user secret
				if userSecretDefined {
					clustercfg.Secret = userSecret
				}

				// Save
				saveConfig(cfg, c.GlobalBool("force"))
				return nil
			},
		},
		{
			Name:   "daemon",
			Usage:  "run the IPFS Cluster peer (default)",
			Action: daemon,
		},
		{
			Name:   "migration",
			Usage:  "migrate the IPFS Cluster state between versions",
			Description: `

This command migrates the internal state of the ipfs-cluster node to the state 
specified in a backup file. The state format is migrated from the version 
of the backup file to the version supported by the current cluster version. 
To succesfully run a migration of an entire cluster, shut down each peer without
removal, migrate using this command, and restart every peer.
`,
			ArgsUsage: "<BACKUP-FILE-PATH> [RAFT-DATA-DIR]",
			Action: func(c *cli.Context) error {
				if c.NArg() < 1 || c.NArg() > 2 {
					return fmt.Errorf("Usage: <BACKUP-FILE-PATH> [RAFT-DATA-DIR]")
					
				}
				//Load configs
	                        cfg, clusterCfg, _, _, consensusCfg, _, _, _ := makeConfigs()
	                        err := cfg.LoadJSONFromFile(configPath)
 	                        checkErr("loading configuration", err)
				backupFilePath := c.Args().First()
				var raftDataPath string
				if c.NArg() == 1 {
					raftDataPath = consensusCfg.DataFolder
				} else {
					raftDataPath = c.Args().Get(1)
				}
				
				//Migrate backup to new state
				backup, err := os.Open(backupFilePath)
				checkErr("opening backup file", err)
				defer backup.Close()
				r := bufio.NewReader(backup)
				newState := mapstate.NewMapState()
				err = newState.Restore(r)
				checkErr("migrating state to alternate version", err)
				// Encode state to save as a snapshot
				newStateBytes, err := encodeState(*newState)
				checkErr("encoding new state", err)
						
				//Remove raft state
				err = cleanupRaft(raftDataPath)
				checkErr("cleaningup Raft", err)
				
				//Write snapshot of the migrated state
				snapshotStore, err := hraft.NewFileSnapshotStoreWithLogger(raftDataPath, 5, nil)
				checkErr("creating snapshot store", err)
				dummyHost, err := makeDummyHost(clusterCfg)
				checkErr("creating dummy host for snapshot store", err)
				dummyTransport, err := libp2praft.NewLibp2pTransport(dummyHost, consensusCfg.NetworkTimeout)
				checkErr("creating dummy transport for snapshot store", err)
				var raftSnapVersion hraft.SnapshotVersion
				raftSnapVersion = 1				// As of v1.0.0 this is always 1
				raftIndex       := uint64(1) // We reset history to the beginning
				raftTerm        := uint64(1) // We reset history to the beginning
				configIndex     := uint64(1) // We reset history to the beginning
				srvCfg := raft.MakeServerConf(dummyHost.Peerstore().Peers())
				sink, err := snapshotStore.Create(raftSnapVersion, raftIndex, raftTerm, srvCfg, configIndex, dummyTransport)  
				checkErr("Creating a temporary snapshot store", err)
				_, err = sink.Write(newStateBytes)
				checkErr("Writing snapshot to sink", err)
				err = sink.Close()
				checkErr("Closing sink", err)

				return nil
			},
		},
	}

	app.Before = func(c *cli.Context) error {
		absPath, err := filepath.Abs(c.String("config"))
		if err != nil {
			return err
		}

		configPath = filepath.Join(absPath, DefaultConfigFile)

		setupLogging(c.String("loglevel"))
		if c.Bool("debug") {
			setupDebug()
		}
		return nil
	}

	app.Action = run

	app.Run(os.Args)
}

// run daemon() by default, or error.
func run(c *cli.Context) error {
	if len(c.Args()) > 0 {
		return fmt.Errorf("Unknown subcommand. Run \"%s help\" for more info", programName)
	}
	return daemon(c)
}

func daemon(c *cli.Context) error {
	// Load all the configurations
	cfg, clusterCfg, apiCfg, ipfshttpCfg, consensusCfg, monCfg, diskInfCfg, numpinInfCfg := makeConfigs()
	err := cfg.LoadJSONFromFile(configPath)
	checkErr("loading configuration", err)

	if a := c.String("bootstrap"); a != "" {
		if len(clusterCfg.Peers) > 0 && !c.Bool("force") {
			return errors.New("the configuration provides cluster.Peers. Use -f to ignore and proceed bootstrapping")
		}
		joinAddr, err := ma.NewMultiaddr(a)
		if err != nil {
			return fmt.Errorf("error parsing multiaddress: %s", err)
		}
		clusterCfg.Bootstrap = []ma.Multiaddr{joinAddr}
		clusterCfg.Peers = []ma.Multiaddr{}
	}

	if c.Bool("leave") {
		clusterCfg.LeaveOnShutdown = true
	}

	api, err := rest.NewAPI(apiCfg)
	checkErr("creating REST API component", err)

	proxy, err := ipfshttp.NewConnector(ipfshttpCfg)
	checkErr("creating IPFS Connector component", err)

	state := mapstate.NewMapState()
	tracker := maptracker.NewMapPinTracker(clusterCfg.ID)
	mon, err := basic.NewMonitor(monCfg)
	checkErr("creating Monitor component", err)
	informer, alloc := setupAllocation(c.String("alloc"), diskInfCfg, numpinInfCfg)

	cluster, err := ipfscluster.NewCluster(
		clusterCfg,
		consensusCfg,
		api,
		proxy,
		state,
		tracker,
		mon,
		alloc,
		informer)
	checkErr("starting cluster", err)

	signalChan := make(chan os.Signal, 20)
	signal.Notify(signalChan,
		syscall.SIGINT,
		syscall.SIGTERM,
		syscall.SIGHUP)
	for {
		select {
		case <-signalChan:
			err = cluster.Shutdown()
			checkErr("shutting down cluster", err)
		case <-cluster.Done():
			return nil

			//case <-cluster.Ready():
		}
	}
	// wait for configuration to be saved
	cfg.Shutdown()
	return nil
}

func cleanupRaft(raftDataDir string) error {
	raftDB := filepath.Join(raftDataDir, "raft.db")
	snapShotDir := filepath.Join(raftDataDir, "snapshots")
	err := os.Remove(raftDB)
	if err != nil {
		return err
	}
	err = os.RemoveAll(snapShotDir)

	// TODO we need to create the snapshot dir so that it can be writeable by the
	// next ipfs-cluster-service process.  Ideally it's not globally writeable for
	// security (escalation to putting any state in ipfs cluster raft logs) but
	// this will need some thought.  What does hraft do so that future processes
	// can edit the snapshot dir after shutting down a raft peer?
	
	return err
}

var facilities = []string{
	"service",
	"cluster",
	"restapi",
	"ipfshttp",
	"monitor",
	"consensus",
	"pintracker",
	"ascendalloc",
	"descendalloc",
	"diskinfo",
	"config",
}

func setupLogging(lvl string) {
	for _, f := range facilities {
		ipfscluster.SetFacilityLogLevel(f, lvl)
	}
}

func setupAllocation(name string, diskInfCfg *disk.Config, numpinInfCfg *numpin.Config) (ipfscluster.Informer, ipfscluster.PinAllocator) {
	switch name {
	case "disk", "disk-freespace":
		informer, err := disk.NewInformer(diskInfCfg)
		checkErr("creating informer", err)
		return informer, descendalloc.NewAllocator()
	case "disk-reposize":
		informer, err := disk.NewInformer(diskInfCfg)
		checkErr("creating informer", err)
		return informer, ascendalloc.NewAllocator()
	case "numpin", "pincount":
		informer, err := numpin.NewInformer(numpinInfCfg)
		checkErr("creating informer", err)
		return informer, ascendalloc.NewAllocator()
	default:
		err := errors.New("unknown allocation strategy")
		checkErr("", err)
		return nil, nil
	}
}

func setupDebug() {
	l := "DEBUG"
	for _, f := range facilities {
		ipfscluster.SetFacilityLogLevel(f, l)
	}

	ipfscluster.SetFacilityLogLevel("p2p-gorpc", l)
	ipfscluster.SetFacilityLogLevel("raft", l)
	//SetFacilityLogLevel("swarm2", l)
	//SetFacilityLogLevel("libp2p-raft", l)
}

func saveConfig(cfg *config.Manager, force bool) {
	if _, err := os.Stat(configPath); err == nil && !force {
		err := fmt.Errorf("%s exists. Try running: %s -f init", configPath, programName)
		checkErr("", err)
	}

	err := os.MkdirAll(filepath.Dir(configPath), 0700)
	err = cfg.SaveJSON(configPath)
	checkErr("saving new configuration", err)
	out("%s configuration written to %s\n",
		programName, configPath)
}

func userProvidedSecret(enterSecret bool) ([]byte, bool) {
	var secret string
	if enterSecret {
		secret = promptUser("Enter cluster secret (32-byte hex string): ")
	} else if envSecret, envSecretDefined := os.LookupEnv("CLUSTER_SECRET"); envSecretDefined {
		secret = envSecret
	} else {
		return nil, false
	}

	decodedSecret, err := ipfscluster.DecodeClusterSecret(secret)
	checkErr("parsing user-provided secret", err)
	return decodedSecret, true
}

func promptUser(msg string) string {
	scanner := bufio.NewScanner(os.Stdin)
	fmt.Print(msg)
	scanner.Scan()
	return scanner.Text()
}

func makeConfigs() (*config.Manager, *ipfscluster.Config, *rest.Config, *ipfshttp.Config, *raft.Config, *basic.Config, *disk.Config, *numpin.Config) {
	cfg := config.NewManager()
	clusterCfg := &ipfscluster.Config{}
	apiCfg := &rest.Config{}
	ipfshttpCfg := &ipfshttp.Config{}
	consensusCfg := &raft.Config{}
	monCfg := &basic.Config{}
	diskInfCfg := &disk.Config{}
	numpinInfCfg := &numpin.Config{}
	cfg.RegisterComponent(config.Cluster, clusterCfg)
	cfg.RegisterComponent(config.API, apiCfg)
	cfg.RegisterComponent(config.IPFSConn, ipfshttpCfg)
	cfg.RegisterComponent(config.Consensus, consensusCfg)
	cfg.RegisterComponent(config.Monitor, monCfg)
	cfg.RegisterComponent(config.Informer, diskInfCfg)
	cfg.RegisterComponent(config.Informer, numpinInfCfg)
	return cfg, clusterCfg, apiCfg, ipfshttpCfg, consensusCfg, monCfg, diskInfCfg, numpinInfCfg
}

func makeDummyHost(cfg *ipfscluster.Config) (host.Host, error) {
	ps := peerstore.NewPeerstore()
	for _, m := range cfg.Peers {
		pid, decapAddr, err := ipfscluster.MultiaddrSplit(m)
		if err != nil {
			return nil, err
		}
		ps.AddAddr(pid, decapAddr, peerstore.PermanentAddrTTL)
	}
	pid, decapAddr, err := ipfscluster.MultiaddrSplit(cfg.ID)
	if err != nil {
		return nil, err
	}
	ps.AddAddr(pid, decapAddr, peerstore.PermanentAddrTTL)
	
	network, err := swarm.NewNetwork(
		context.Background(),
		[]ma.Multiaddr{cfg.ListenAddr},
		cfg.ID,
		ps,
		nil,
	)
	if err != nil {
		return nil, err
	}
		
	bhost := basichost.New(network)
	return bhost, nil
}
	
func encodeState(state mapstate.MapState) ([]byte, error) {
        buf := new(bytes.Buffer)
        enc := msgpack.Multicodec(msgpack.DefaultMsgpackHandle()).Encoder(buf)
        if err := enc.Encode(state); err != nil {
                return nil, err
        }
        return buf.Bytes(), nil
}
