package pb

import (
	"log"
	"net/http"
	"os"
	"strconv"

	"github.com/labstack/echo/v5"
	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/models"
	"github.com/pocketbase/pocketbase/plugins/migratecmd"
	"github.com/seriousm4x/upsnap/backend/cronjobs"
	"github.com/seriousm4x/upsnap/backend/logger"
	_ "github.com/seriousm4x/upsnap/backend/migrations"
)

var App *pocketbase.PocketBase

func StartPocketBase() {
	App = pocketbase.New()

	// auto migrate db
	migratecmd.MustRegister(App, App.RootCmd, &migratecmd.Options{
		Automigrate: true,
	})

	// event hooks
	App.OnBeforeServe().Add(func(e *core.ServeEvent) error {
		// set static website path
		e.Router.GET("/*", apis.StaticDirectoryHandler(os.DirFS("./pb_public"), true))

		// add wake route to api
		e.Router.AddRoute(echo.Route{
			Method:  http.MethodGet,
			Path:    "/api/upsnap/wake/:id",
			Handler: HandlerWake,
			Middlewares: []echo.MiddlewareFunc{
				apis.ActivityLogger(App),
			},
		})

		// add shutdown route to api
		e.Router.AddRoute(echo.Route{
			Method:  http.MethodGet,
			Path:    "/api/upsnap/shutdown/:id",
			Handler: HandlerShutdown,
			Middlewares: []echo.MiddlewareFunc{
				apis.ActivityLogger(App),
			},
		})

		// add network scan route to api
		e.Router.AddRoute(echo.Route{
			Method:  http.MethodGet,
			Path:    "/api/upsnap/scan",
			Handler: HandlerScan,
			Middlewares: []echo.MiddlewareFunc{
				apis.ActivityLogger(App),
			},
		})

		// import environment and set settings
		if err := importSettings(); err != nil {
			return err
		}

		// reset device states and run ping cronjob
		if err := resetDeviceStates(); err != nil {
			return err
		}

		// run ping cronjob
		go cronjobs.RunCron(App)

		// add event hook before starting server.
		// using this outside App.OnBeforeServe() would not work
		App.OnModelAfterUpdate().Add(func(e *core.ModelEvent) error {
			if e.Model.TableName() == "settings" {
				for _, job := range cronjobs.Jobs.Entries() {
					cronjobs.Jobs.Remove(job.ID)
				}
				go cronjobs.RunCron(App)
			} else {
				refreshDeviceList()
			}
			return nil
		})

		return nil
	})

	// refresh the device list on database events
	App.OnModelAfterCreate().Add(func(e *core.ModelEvent) error {
		refreshDeviceList()
		return nil
	})
	App.OnModelAfterDelete().Add(func(e *core.ModelEvent) error {
		refreshDeviceList()
		return nil
	})

	// start pocketbase
	if err := App.Start(); err != nil {
		log.Fatal(err)
	}
}

func importSettings() error {
	// get first settings record
	settingsRecords, err := App.Dao().FindRecordsByExpr("settings")
	if err != nil {
		return err
	}
	settingsCollection, err := App.Dao().FindCollectionByNameOrId("settings")
	if err != nil {
		return err
	}
	settings := models.NewRecord(settingsCollection)
	if len(settingsRecords) > 0 {
		settings = settingsRecords[0]
	}

	// set ping interval and notification settings. priority:
	// 1st: env var
	// 2nd: database entry
	// 3rd: default values
	interval := "@every 3s"
	if settings.GetString("interval") != "" {
		interval = settings.GetString("interval")
	}
	if os.Getenv("UPSNAP_INTERVAL") != "" {
		interval = os.Getenv("UPSNAP_INTERVAL")
	}
	notifications := true
	if settings.GetBool("notifications") {
		notifications = settings.GetBool("notifications")
	}
	if os.Getenv("UPSNAP_NOTIFICATIONS") != "" {
		notificationsParsed, err := strconv.ParseBool(os.Getenv("UPSNAP_NOTIFICATIONS"))
		if err != nil {
			return err
		} else {
			notifications = notificationsParsed
		}
	}

	// save settings to db
	settings.Set("interval", interval)
	settings.Set("notifications", notifications)
	if err := App.Dao().SaveRecord(settings); err != nil {
		return err
	}

	logger.Debug.Println("Ping interval set to", interval)
	logger.Debug.Println("Notifications set to", notifications)
	return nil
}

func resetDeviceStates() error {
	devices, err := App.Dao().FindRecordsByExpr("devices")
	if err != nil {
		return err
	}
	for _, device := range devices {
		device.Set("status", "offline")
		if err := App.Dao().SaveRecord(device); err != nil {
			return err
		}
	}
	cronjobs.Devices = devices
	return nil
}

func refreshDeviceList() {
	var err error
	cronjobs.Devices, err = App.Dao().FindRecordsByExpr("devices")
	if err != nil {
		logger.Error.Println(err)
	}
}
