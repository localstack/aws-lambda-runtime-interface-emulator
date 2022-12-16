// main entrypoint of init
// initial structure based upon /cmd/aws-lambda-rie/main.go
package main

import (
	"context"
	log "github.com/sirupsen/logrus"
	"go.amzn.com/lambda/rapidcore"
	"net/http"
	_ "net/http/pprof"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
)

type LsOpts struct {
	InteropPort       string
	RuntimeEndpoint   string
	RuntimeId         string
	InitTracingPort   string
	CodeDownloadUrl   string
	HotReloadingPaths []string
}

func GetEnvOrDie(env string) string {
	result, found := os.LookupEnv(env)
	if !found {
		panic("Could not find environment variable for: " + env)
	}
	return result
}

func InitLsOpts() *LsOpts {
	return &LsOpts{
		RuntimeEndpoint: GetEnvOrDie("LOCALSTACK_RUNTIME_ENDPOINT"),
		RuntimeId:       GetEnvOrDie("LOCALSTACK_RUNTIME_ID"),
		// optional with default
		InteropPort:     GetenvWithDefault("LOCALSTACK_INTEROP_PORT", "9563"),
		InitTracingPort: GetenvWithDefault("LOCALSTACK_RUNTIME_TRACING_PORT", "9564"),
		// optional or empty
		CodeDownloadUrl:   os.Getenv("LOCALSTACK_CODE_ARCHIVE_DOWNLOAD_URL"),
		HotReloadingPaths: strings.Split(GetenvWithDefault("LOCALSTACK_HOT_RELOADING_PATHS", ""), ","),
	}
}

func main() {
	// we're setting this to the same value as in the official RIE
	debug.SetGCPercent(33)

	lsOpts := InitLsOpts()

	// set up logging (logrus)
	//log.SetFormatter(&log.JSONFormatter{})
	//log.SetLevel(log.TraceLevel)
	log.SetLevel(log.DebugLevel)
	log.SetReportCaller(true)
	// download code archive if env variable is set
	DownloadCodeArchive(lsOpts.CodeDownloadUrl)
	// parse CLI args
	opts, args := getCLIArgs()
	bootstrap, handler := getBootstrap(args, opts)
	logCollector := NewLogCollector()
	fileWatcherContext, cancelFileWatcher := context.WithCancel(context.Background())
	sandbox := rapidcore.
		NewSandboxBuilder(bootstrap).
		AddShutdownFunc(func() {
			log.Debugln("Closing file watcher")
			cancelFileWatcher()
		}).
		AddShutdownFunc(func() { os.Exit(0) }).
		SetExtensionsFlag(true).
		SetInitCachingFlag(true).
		SetTailLogOutput(logCollector)

	defaultInterop := sandbox.InteropServer()
	interopServer := NewCustomInteropServer(lsOpts, defaultInterop, logCollector)
	sandbox.SetInteropServer(interopServer)
	if len(handler) > 0 {
		sandbox.SetHandler(handler)
	}

	// initialize all flows and start runtime API
	go sandbox.Create()

	// get timeout
	invokeTimeoutEnv := GetEnvOrDie("AWS_LAMBDA_FUNCTION_TIMEOUT")
	invokeTimeoutSeconds, err := strconv.Atoi(invokeTimeoutEnv)
	if err != nil {
		log.Fatalln(err)
	}
	go RunHotReloadingListener(interopServer, lsOpts.HotReloadingPaths, fileWatcherContext)

	// start runtime init
	go InitHandler(sandbox, GetEnvOrDie("AWS_LAMBDA_FUNCTION_VERSION"), int64(invokeTimeoutSeconds)) // TODO: replace this with a custom init

	// TODO: make the tracing server optional
	// start blocking with the tracing server
	err = http.ListenAndServe("0.0.0.0:"+lsOpts.InitTracingPort, http.DefaultServeMux)
	if err != nil {
		log.Fatal("Failed to start debug server")
	}
}
