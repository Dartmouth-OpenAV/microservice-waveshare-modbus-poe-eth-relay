package main

import (
	"errors"

	"github.com/Dartmouth-OpenAV/microservice-framework/framework"
)

func setFrameworkGlobals() {
	framework.MicroserviceName = "OpenAV Waveshare Relay Microservice"
	// Define device specific globals
	framework.DefaultSocketPort = 4196 // Waveshare's default Modbus RTU over TCP port (use :502 for Modbus TCP mode)

	// globals that change modes in the microservice framework:
	framework.CheckFunctionAppendBehavior = "Remove older instance"

	framework.RegisterMainGetFunc(doDeviceSpecificGet)
	framework.RegisterMainSetFunc(doDeviceSpecificSet)
}

// Every microservice using this golang microservice framework needs to provide this function to invoke functions to do sets.
// socketKey is the network connection for the framework to use to communicate with the device.
// setting is the first parameter in the URI.
// arg1 and arg2 are the second and third parameters in the URI, followed by the PUT body.
//
//	Example PUT URIs that will result in this function being invoked:
//	":address/:setting/"
//	":address/:setting/:arg1"
//	":address/:setting/:arg1/:arg2"
func doDeviceSpecificSet(socketKey string, setting string, arg1 string, arg2 string, arg3 string) (string, error) {
	function := "doDeviceSpecificSet"

	switch setting {
	case "state":
		return setState(socketKey, arg1, arg2) // relay, "on"/"off"
	case "allstates":
		return setAllStates(socketKey, arg1) // "on"/"off"
	case "trigger":
		return setTrigger(socketKey, arg1, arg2) // relay, seconds
	case "timedtrigger":
		return setTimedTrigger(socketKey, arg1, arg2, arg3) // relay1, relay2, seconds
	}

	// If we get here, we didn't recognize the setting.  Send an error back to the config writer who had a bad URL.
	errMsg := function + " - unrecognized setting in URI: " + setting
	framework.AddToErrors(socketKey, errMsg)
	err := errors.New(errMsg)
	return setting, err
}

// Every microservice using this golang microservice framework needs to provide this function to invoke functions to do gets.
// socketKey is the network connection for the framework to use to communicate with the device.
// setting is the first parameter in the URI.
// arg1 and arg2 are the second and third parameters in the URI.
//
//	Example GET URIs that will result in this function being invoked:
//	":address/:setting/"
//	":address/:setting/:arg1"
//	":address/:setting/:arg1/:arg2"
func doDeviceSpecificGet(socketKey string, setting string, arg1 string, arg2 string) (string, error) {
	function := "doDeviceSpecificGet"

	switch setting {
	case "state":
		return getState(socketKey, arg1) // relay
	case "states":
		return getStates(socketKey)
	case "healthcheck":
		return healthCheck(socketKey)
	}

	// If we get here, we didn't recognize the setting.  Send an error back to the config writer who had a bad URL.
	errMsg := function + " - unrecognized setting in URI: " + setting
	framework.AddToErrors(socketKey, errMsg)
	err := errors.New(errMsg)
	return setting, err
}

func main() {
	setFrameworkGlobals()
	framework.Startup()
}
