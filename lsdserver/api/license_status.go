// Copyright (c) 2016 Readium Foundation
//
// Redistribution and use in source and binary forms, with or without modification,
// are permitted provided that the following conditions are met:
//
// 1. Redistributions of source code must retain the above copyright notice, this
//    list of conditions and the following disclaimer.
// 2. Redistributions in binary form must reproduce the above copyright notice,
//    this list of conditions and the following disclaimer in the documentation and/or
//    other materials provided with the distribution.
// 3. Neither the name of the organization nor the names of its contributors may be
//    used to endorse or promote products derived from this software without specific
//    prior written permission
//
// THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS "AS IS" AND
// ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO, THE IMPLIED
// WARRANTIES OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR PURPOSE ARE
// DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT OWNER OR CONTRIBUTORS BE LIABLE FOR
// ANY DIRECT, INDIRECT, INCIDENTAL, SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES
// (INCLUDING, BUT NOT LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES;
// LOSS OF USE, DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND
// ON ANY THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
// (INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE OF THIS
// SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.

package apilsd

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"

	"github.com/readium/readium-lcp-server/api"
	"github.com/readium/readium-lcp-server/config"
	"github.com/readium/readium-lcp-server/lcpserver/api"
	"github.com/readium/readium-lcp-server/license"
	"github.com/readium/readium-lcp-server/license_statuses"
	"github.com/readium/readium-lcp-server/localization"
	"github.com/readium/readium-lcp-server/logging"
	"github.com/readium/readium-lcp-server/problem"
	"github.com/readium/readium-lcp-server/status"
	"github.com/readium/readium-lcp-server/transactions"
)

type Server interface {
	Transactions() transactions.Transactions
	LicenseStatuses() licensestatuses.LicenseStatuses
}

//CreateLicenseStatusDocument create license status and add it to database
func CreateLicenseStatusDocument(w http.ResponseWriter, r *http.Request, s Server) {
	var lic license.License
	err := apilcp.DecodeJsonLicense(r, &lic)

	if err != nil {
		problem.Error(w, r, problem.Problem{Detail: err.Error()}, http.StatusBadRequest)
		return
	}

	var ls licensestatuses.LicenseStatus
	makeLicenseStatus(lic, &ls)

	err = s.LicenseStatuses().Add(ls)
	if err != nil {
		problem.Error(w, r, problem.Problem{Detail: err.Error()}, http.StatusInternalServerError)
		return
	}

	// must come *after* w.Header().Add()/Set(), but before w.Write()
	w.WriteHeader(http.StatusCreated)
}

//GetLicenseStatusDocument get license status from database by licese id
//checks potential_rights_end and fill it
func GetLicenseStatusDocument(w http.ResponseWriter, r *http.Request, s Server) {
	vars := mux.Vars(r)

	licenseFk := vars["key"]

	licenseStatus, err := s.LicenseStatuses().GetByLicenseId(licenseFk)
	if err != nil {
		if licenseStatus == nil {
			problem.NotFoundHandler(w, r)
			logging.WriteToFile(complianceTestNumber, LICENSE_STATUS, strconv.Itoa(http.StatusNotFound))
			return
		}

		problem.Error(w, r, problem.Problem{Detail: err.Error()}, http.StatusInternalServerError)
		logging.WriteToFile(complianceTestNumber, LICENSE_STATUS, strconv.Itoa(http.StatusInternalServerError))
		return
	}

	currentDateTime := time.Now()

	if licenseStatus.PotentialRights != nil && licenseStatus.PotentialRights.End != nil && !(*licenseStatus.PotentialRights.End).IsZero() {
		diff := currentDateTime.Sub(*(licenseStatus.PotentialRights.End))

		if (diff > 0) && ((licenseStatus.Status == status.STATUS_ACTIVE) || (licenseStatus.Status == status.STATUS_READY)) {
			licenseStatus.Status = status.STATUS_EXPIRED
			err = s.LicenseStatuses().Update(*licenseStatus)
			if err != nil {
				problem.Error(w, r, problem.Problem{Detail: err.Error()}, http.StatusInternalServerError)
				logging.WriteToFile(complianceTestNumber, LICENSE_STATUS, strconv.Itoa(http.StatusInternalServerError))
				return
			}
		}
	}

	err = fillLicenseStatus(licenseStatus, r, s)
	if err != nil {
		problem.Error(w, r, problem.Problem{Detail: err.Error()}, http.StatusInternalServerError)
		logging.WriteToFile(complianceTestNumber, LICENSE_STATUS, strconv.Itoa(http.StatusInternalServerError))
		return
	}

	w.Header().Set("Content-Type", api.ContentType_LSD_JSON)

	licenseStatus.DeviceCount = nil
	enc := json.NewEncoder(w)
	err = enc.Encode(licenseStatus)

	if err != nil {
		problem.Error(w, r, problem.Problem{Detail: err.Error()}, http.StatusInternalServerError)
		logging.WriteToFile(complianceTestNumber, LICENSE_STATUS, strconv.Itoa(http.StatusInternalServerError))
		return
	}

	logging.WriteToFile(complianceTestNumber, LICENSE_STATUS, strconv.Itoa(http.StatusOK))
}

//RegisterDevice register device using device id & device name request parameters
//& returns updated and filled license status
func RegisterDevice(w http.ResponseWriter, r *http.Request, s Server) {
	w.Header().Set("Content-Type", api.ContentType_LSD_JSON)
	vars := mux.Vars(r)

	licenseFk := vars["key"]
	licenseStatus, err := s.LicenseStatuses().GetByLicenseId(licenseFk)

	if err != nil {
		if licenseStatus == nil {
			problem.NotFoundHandler(w, r)
			logging.WriteToFile(complianceTestNumber, REGISTER_DEVICE, strconv.Itoa(http.StatusNotFound))
			return
		}

		problem.Error(w, r, problem.Problem{Detail: err.Error()}, http.StatusInternalServerError)
		logging.WriteToFile(complianceTestNumber, REGISTER_DEVICE, strconv.Itoa(http.StatusInternalServerError))
		return
	}

	deviceId := r.FormValue("id")
	deviceName := r.FormValue("name")

	dILen := len(deviceId)
	dNLen := len(deviceName)

	//check mandatory request parameters
	if (dILen == 0) || (dILen > 255) || (dNLen == 0) || (dNLen > 255) {
		problem.Error(w, r, problem.Problem{Detail: "device id and device name are mandatory and maximum length is 255 symbols "}, http.StatusBadRequest)
		logging.WriteToFile(complianceTestNumber, REGISTER_DEVICE, strconv.Itoa(http.StatusBadRequest))
		return
	}

	//check status of license status
	if (licenseStatus.Status != status.STATUS_ACTIVE) && (licenseStatus.Status != status.STATUS_READY) {
		problem.Error(w, r, problem.Problem{Detail: "License is not active"}, http.StatusBadRequest)
		logging.WriteToFile(complianceTestNumber, REGISTER_DEVICE, strconv.Itoa(http.StatusBadRequest))
		return
	}

	//check the existence of device in license status
	deviceStatus, err := s.Transactions().CheckDeviceStatus(licenseStatus.Id, deviceId)
	if err != nil {
		problem.Error(w, r, problem.Problem{Detail: err.Error()}, http.StatusInternalServerError)
		logging.WriteToFile(complianceTestNumber, REGISTER_DEVICE, strconv.Itoa(http.StatusInternalServerError))
		return
	}
	if deviceStatus != "" { // deviceStatus == status.TYPE_REGISTER || deviceStatus == status.TYPE_RETURN || deviceStatus == status.TYPE_RENEW
		problem.Error(w, r, problem.Problem{Detail: "Device has been already registered"}, http.StatusBadRequest)
		logging.WriteToFile(complianceTestNumber, REGISTER_DEVICE, strconv.Itoa(http.StatusBadRequest))
		return
	}

	//make event for register transaction
	event := makeEvent(status.TYPE_REGISTER, deviceName, deviceId, licenseStatus.Id)

	err = s.Transactions().Add(*event, 1)
	if err != nil {
		problem.Error(w, r, problem.Problem{Detail: err.Error()}, http.StatusInternalServerError)
		logging.WriteToFile(complianceTestNumber, REGISTER_DEVICE, strconv.Itoa(http.StatusInternalServerError))
		return
	}

	licenseStatus.Updated.Status = &event.Timestamp

	//check & set the status of the license status
	if licenseStatus.Status == status.STATUS_READY {
		licenseStatus.Status = status.STATUS_ACTIVE
	}

	*licenseStatus.DeviceCount += 1

	err = s.LicenseStatuses().Update(*licenseStatus)
	if err != nil {
		problem.Error(w, r, problem.Problem{Detail: err.Error()}, http.StatusInternalServerError)
		logging.WriteToFile(complianceTestNumber, REGISTER_DEVICE, strconv.Itoa(http.StatusInternalServerError))
		return
	}

	//fill license status
	err = fillLicenseStatus(licenseStatus, r, s)
	if err != nil {
		problem.Error(w, r, problem.Problem{Detail: err.Error()}, http.StatusInternalServerError)
		logging.WriteToFile(complianceTestNumber, REGISTER_DEVICE, strconv.Itoa(http.StatusInternalServerError))
		return
	}

	licenseStatus.DeviceCount = nil
	enc := json.NewEncoder(w)
	err = enc.Encode(licenseStatus)
	if err != nil {
		problem.Error(w, r, problem.Problem{Detail: err.Error()}, http.StatusInternalServerError)
		logging.WriteToFile(complianceTestNumber, REGISTER_DEVICE, strconv.Itoa(http.StatusInternalServerError))
		return
	}
	logging.WriteToFile(complianceTestNumber, REGISTER_DEVICE, strconv.Itoa(http.StatusOK))
}

//LendingReturn checks that the calling device is activated, then modifies
//the end date associated with the given license & returns updated and filled license status
func LendingReturn(w http.ResponseWriter, r *http.Request, s Server) {
	w.Header().Set("Content-Type", api.ContentType_LSD_JSON)
	vars := mux.Vars(r)

	licenseFk := vars["key"]
	licenseStatus, err := s.LicenseStatuses().GetByLicenseId(licenseFk)

	if err != nil {
		if licenseStatus == nil {
			problem.NotFoundHandler(w, r)
			logging.WriteToFile(complianceTestNumber, RETURN_LICENSE, strconv.Itoa(http.StatusNotFound))
			return
		}

		problem.Error(w, r, problem.Problem{Detail: err.Error()}, http.StatusInternalServerError)
		logging.WriteToFile(complianceTestNumber, RETURN_LICENSE, strconv.Itoa(http.StatusInternalServerError))
		return
	}

	deviceId := r.FormValue("id")
	deviceName := r.FormValue("name")

	//checks request parameters
	if (len(deviceName) > 255) || (len(deviceId) > 255) {
		problem.Error(w, r, problem.Problem{Detail: err.Error()}, http.StatusBadRequest)
		logging.WriteToFile(complianceTestNumber, RETURN_LICENSE, strconv.Itoa(http.StatusBadRequest))
		return
	}

	//check & set the status of license status according to its current value
	switch licenseStatus.Status {
	case status.STATUS_RETURNED:
		problem.Error(w, r, problem.Problem{Detail: "License has been already returned"}, http.StatusForbidden)
		logging.WriteToFile(complianceTestNumber, RETURN_LICENSE, strconv.Itoa(http.StatusForbidden))
		return
	case status.STATUS_EXPIRED:
		problem.Error(w, r, problem.Problem{Detail: "License is expired"}, http.StatusForbidden)
		logging.WriteToFile(complianceTestNumber, RETURN_LICENSE, strconv.Itoa(http.StatusForbidden))
		return
	case status.STATUS_ACTIVE:
		licenseStatus.Status = status.STATUS_RETURNED
		break
	case status.STATUS_READY:
		licenseStatus.Status = status.STATUS_CANCELLED
		break
	case status.STATUS_CANCELLED:
		problem.Error(w, r, problem.Problem{Detail: "License is cancelled"}, http.StatusForbidden)
		logging.WriteToFile(complianceTestNumber, RETURN_LICENSE, strconv.Itoa(http.StatusForbidden))
		return
	case status.STATUS_REVOKED:
		problem.Error(w, r, problem.Problem{Detail: "License is revoked"}, http.StatusForbidden)
		logging.WriteToFile(complianceTestNumber, RETURN_LICENSE, strconv.Itoa(http.StatusForbidden))
		return
	}

	//check if device is activated
	if deviceId != "" {
		deviceStatus, err := s.Transactions().CheckDeviceStatus(licenseStatus.Id, deviceId)
		if err != nil {
			problem.Error(w, r, problem.Problem{Detail: err.Error()}, http.StatusInternalServerError)
			logging.WriteToFile(complianceTestNumber, RETURN_LICENSE, strconv.Itoa(http.StatusInternalServerError))
			return
		}
		if deviceStatus == status.TYPE_RETURN || deviceStatus == "" { // deviceStatus != status.TYPE_REGISTER && deviceStatus != status.TYPE_RENEW
			problem.Error(w, r, problem.Problem{Detail: "Device is not activated"}, http.StatusBadRequest)
			logging.WriteToFile(complianceTestNumber, RETURN_LICENSE, strconv.Itoa(http.StatusBadRequest))
			return
		}
	}

	//create event for lending return
	event := makeEvent(status.TYPE_RETURN, deviceName, deviceId, licenseStatus.Id)

	err = s.Transactions().Add(*event, 2)
	if err != nil {
		problem.Error(w, r, problem.Problem{Detail: err.Error()}, http.StatusInternalServerError)
		logging.WriteToFile(complianceTestNumber, RETURN_LICENSE, strconv.Itoa(http.StatusInternalServerError))
		return
	}

	//update license using LCP Server
	httpStatusCode, errorr := updateLicense(event.Timestamp, licenseFk)
	if errorr != nil {
		problem.Error(w, r, problem.Problem{Detail: errorr.Error()}, http.StatusInternalServerError)
		logging.WriteToFile(complianceTestNumber, RETURN_LICENSE, strconv.Itoa(http.StatusInternalServerError))
		return
	}
	if httpStatusCode != http.StatusOK && httpStatusCode != http.StatusPartialContent { // 200, 206
		errorr = errors.New("LCP license PATCH returned HTTP error code " + strconv.Itoa(httpStatusCode))

		problem.Error(w, r, problem.Problem{Detail: errorr.Error()}, httpStatusCode)
		logging.WriteToFile(complianceTestNumber, RETURN_LICENSE, strconv.Itoa(httpStatusCode))
		return
	}
	licenseStatus.CurrentEndLicense = &event.Timestamp

	//update licenseStatus
	licenseStatus.Updated.Status = &event.Timestamp
	licenseStatus.Updated.License = &event.Timestamp

	err = s.LicenseStatuses().Update(*licenseStatus)
	if err != nil {
		problem.Error(w, r, problem.Problem{Detail: err.Error()}, http.StatusInternalServerError)
		logging.WriteToFile(complianceTestNumber, RETURN_LICENSE, strconv.Itoa(http.StatusInternalServerError))
		return
	}

	//fill license status
	err = fillLicenseStatus(licenseStatus, r, s)
	if err != nil {
		problem.Error(w, r, problem.Problem{Detail: err.Error()}, http.StatusInternalServerError)
		logging.WriteToFile(complianceTestNumber, RETURN_LICENSE, strconv.Itoa(http.StatusInternalServerError))
		return
	}

	licenseStatus.DeviceCount = nil
	enc := json.NewEncoder(w)
	err = enc.Encode(licenseStatus)

	if err != nil {
		problem.Error(w, r, problem.Problem{Detail: err.Error()}, http.StatusInternalServerError)
		logging.WriteToFile(complianceTestNumber, RETURN_LICENSE, strconv.Itoa(http.StatusInternalServerError))
		return
	}

	logging.WriteToFile(complianceTestNumber, RETURN_LICENSE, strconv.Itoa(http.StatusOK))
}

//LendingRenewal checks that the calling device is activated, then modifies
//the end date associated with the license & returns updated and filled license status
func LendingRenewal(w http.ResponseWriter, r *http.Request, s Server) {
	w.Header().Set("Content-Type", api.ContentType_LSD_JSON)
	vars := mux.Vars(r)

	licenseFk := vars["key"]
	licenseStatus, err := s.LicenseStatuses().GetByLicenseId(licenseFk)

	if err != nil {
		if licenseStatus == nil {
			problem.NotFoundHandler(w, r)
			logging.WriteToFile(complianceTestNumber, RENEW_LICENSE, strconv.Itoa(http.StatusNotFound))
			return
		}
		problem.Error(w, r, problem.Problem{Detail: err.Error()}, http.StatusInternalServerError)
		logging.WriteToFile(complianceTestNumber, RENEW_LICENSE, strconv.Itoa(http.StatusInternalServerError))
		return
	}

	deviceId := r.FormValue("id")
	deviceName := r.FormValue("name")

	//check the request parameters
	if (len(deviceName) > 255) || (len(deviceId) > 255) {
		problem.Error(w, r, problem.Problem{Detail: err.Error()}, http.StatusBadRequest)
		logging.WriteToFile(complianceTestNumber, RENEW_LICENSE, strconv.Itoa(http.StatusBadRequest))
		return
	}

	if (licenseStatus.Status != status.STATUS_ACTIVE) && (licenseStatus.Status != status.STATUS_READY) {
		problem.Error(w, r, problem.Problem{Detail: "License is not active"}, http.StatusBadRequest)
		logging.WriteToFile(complianceTestNumber, RENEW_LICENSE, strconv.Itoa(http.StatusBadRequest))
		return
	}

	//check if device is active
	if deviceId != "" {
		deviceStatus, err := s.Transactions().CheckDeviceStatus(licenseStatus.Id, deviceId)
		if err != nil {
			problem.Error(w, r, problem.Problem{Detail: err.Error()}, http.StatusInternalServerError)
			logging.WriteToFile(complianceTestNumber, RENEW_LICENSE, strconv.Itoa(http.StatusInternalServerError))
			return
		}
		if deviceStatus != status.TYPE_REGISTER && deviceStatus != status.TYPE_RENEW { // deviceStatus == "" || deviceStatus == status.TYPE_RETURN
			problem.Error(w, r, problem.Problem{Detail: "The device is not active for this license"}, http.StatusBadRequest)
			logging.WriteToFile(complianceTestNumber, RENEW_LICENSE, strconv.Itoa(http.StatusBadRequest))
			return
		}
	}

	if licenseStatus.PotentialRights == nil || licenseStatus.PotentialRights.End == nil || (*licenseStatus.PotentialRights.End).IsZero() {
		problem.Error(w, r, problem.Problem{Detail: "Potential rights end not set"}, http.StatusInternalServerError)
		logging.WriteToFile(complianceTestNumber, RENEW_LICENSE, strconv.Itoa(http.StatusInternalServerError))
		return
	}

	var suggestedEnd time.Time
	//suggestedEnd = time.Now() // isZero() is default value

	//set new date for potential_rights_end
	//if request parameter 'end' is empty, it used renew_days parameter from config
	timeEndString := r.FormValue("end")
	if timeEndString == "" {
		renewDays := config.Config.LicenseStatus.RenewDays
		if renewDays == 0 {
			problem.Error(w, r, problem.Problem{Detail: "renew_days not found"}, http.StatusInternalServerError)
			logging.WriteToFile(complianceTestNumber, RENEW_LICENSE, strconv.Itoa(http.StatusInternalServerError))
			return
		}

		var suggestedDuration time.Duration
		//suggestedDuration = time.Duration(0)

		suggestedDuration = 24 * time.Hour * time.Duration(renewDays) // nanoseconds

		if licenseStatus.CurrentEndLicense != nil && !(*licenseStatus.CurrentEndLicense).IsZero() {
			suggestedEnd = (*licenseStatus.CurrentEndLicense).Add(time.Duration(suggestedDuration))
		} else {
			//suggestedEnd = time.Now().Add(time.Duration(suggestedDuration))

			problem.Error(w, r, problem.Problem{Detail: "CurrentEndLicense for LSD License Status is not set"}, http.StatusInternalServerError)
			logging.WriteToFile(complianceTestNumber, RENEW_LICENSE, strconv.Itoa(http.StatusInternalServerError))
			return
		}
	} else {
		expirationEnd, err := time.Parse(time.RFC3339, timeEndString)
		if err != nil {
			problem.Error(w, r, problem.Problem{Detail: err.Error()}, http.StatusInternalServerError)
			logging.WriteToFile(complianceTestNumber, RENEW_LICENSE, strconv.Itoa(http.StatusInternalServerError))
			return
		}

		suggestedEnd = expirationEnd
	}

	if suggestedEnd.After(*licenseStatus.PotentialRights.End) {
		problem.Error(w, r, problem.Problem{Detail: "attempt to renew with date greater than potential rights end"}, http.StatusForbidden)
		logging.WriteToFile(complianceTestNumber, RENEW_LICENSE, strconv.Itoa(http.StatusForbidden))
		return
	}

	if suggestedEnd.Before(time.Now()) {
		problem.Error(w, r, problem.Problem{Detail: "attempt to renew with date before now"}, http.StatusForbidden)
		logging.WriteToFile(complianceTestNumber, RENEW_LICENSE, strconv.Itoa(http.StatusForbidden))
		return
	}

	event := makeEvent(status.TYPE_RENEW, deviceName, deviceId, licenseStatus.Id)

	err = s.Transactions().Add(*event, 3)
	if err != nil {
		problem.Error(w, r, problem.Problem{Detail: err.Error()}, http.StatusInternalServerError)
		logging.WriteToFile(complianceTestNumber, RENEW_LICENSE, strconv.Itoa(http.StatusInternalServerError))
		return
	}

	//update license using LCP Server
	httpStatusCode, errorr := updateLicense(suggestedEnd, licenseFk)
	if errorr != nil {
		problem.Error(w, r, problem.Problem{Detail: errorr.Error()}, http.StatusInternalServerError)
		logging.WriteToFile(complianceTestNumber, RENEW_LICENSE, strconv.Itoa(http.StatusInternalServerError))
		return
	}
	if httpStatusCode != http.StatusOK && httpStatusCode != http.StatusPartialContent { // 200, 206
		errorr = errors.New("LCP license PATCH returned HTTP error code " + strconv.Itoa(httpStatusCode))

		problem.Error(w, r, problem.Problem{Detail: errorr.Error()}, httpStatusCode)
		logging.WriteToFile(complianceTestNumber, RENEW_LICENSE, strconv.Itoa(httpStatusCode))
		return
	}
	licenseStatus.CurrentEndLicense = &suggestedEnd

	//update license status fields
	licenseStatus.Updated.Status = &event.Timestamp
	licenseStatus.Updated.License = &event.Timestamp
	licenseStatus.Status = status.STATUS_ACTIVE

	err = s.LicenseStatuses().Update(*licenseStatus)
	if err != nil {
		problem.Error(w, r, problem.Problem{Detail: err.Error()}, http.StatusInternalServerError)
		logging.WriteToFile(complianceTestNumber, RENEW_LICENSE, strconv.Itoa(http.StatusInternalServerError))
		return
	}

	err = fillLicenseStatus(licenseStatus, r, s)
	if err != nil {
		problem.Error(w, r, problem.Problem{Detail: err.Error()}, http.StatusInternalServerError)
		logging.WriteToFile(complianceTestNumber, RENEW_LICENSE, strconv.Itoa(http.StatusInternalServerError))
		return
	}

	licenseStatus.DeviceCount = nil
	enc := json.NewEncoder(w)
	err = enc.Encode(licenseStatus)

	if err != nil {
		problem.Error(w, r, problem.Problem{Detail: err.Error()}, http.StatusInternalServerError)
		logging.WriteToFile(complianceTestNumber, RENEW_LICENSE, strconv.Itoa(http.StatusInternalServerError))
		return
	}

	logging.WriteToFile(complianceTestNumber, RENEW_LICENSE, strconv.Itoa(http.StatusOK))
}

//FilterLicenseStatuses returns a sequence of license statuses, in their id order
//function for detecting licenses which used a lot of devices
func FilterLicenseStatuses(w http.ResponseWriter, r *http.Request, s Server) {
	w.Header().Set("Content-Type", api.ContentType_JSON)

	// Get request parameters. If not defined, set default values
	rDevices := r.FormValue("devices")
	if rDevices == "" {
		rDevices = "1"
	}

	rPage := r.FormValue("page")
	if rPage == "" {
		rPage = "1"
	}

	rPerPage := r.FormValue("per_page")
	if rPerPage == "" {
		rPerPage = "10"
	}

	devicesLimit, err := strconv.ParseInt(rDevices, 10, 32)
	if err != nil {
		problem.Error(w, r, problem.Problem{Detail: err.Error()}, http.StatusBadRequest)
		return
	}

	page, err := strconv.ParseInt(rPage, 10, 32)
	if err != nil {
		problem.Error(w, r, problem.Problem{Detail: err.Error()}, http.StatusBadRequest)
		return
	}

	perPage, err := strconv.ParseInt(rPerPage, 10, 32)
	if err != nil {
		problem.Error(w, r, problem.Problem{Detail: err.Error()}, http.StatusBadRequest)
		return
	}

	if (page < 1) || (perPage < 1) || (devicesLimit < 1) {
		problem.Error(w, r, problem.Problem{Detail: "Devices, page, per_page must be positive number"}, http.StatusBadRequest)
		return
	}

	page -= 1

	licenseStatuses := make([]licensestatuses.LicenseStatus, 0)

	fn := s.LicenseStatuses().List(devicesLimit, perPage, page*perPage)
	for it, err := fn(); err == nil; it, err = fn() {
		licenseStatuses = append(licenseStatuses, it)
	}

	devices := strconv.Itoa(int(devicesLimit))
	lsperpage := strconv.Itoa(int(perPage) + 1)
	var resultLink string

	if len(licenseStatuses) > 0 {
		nextPage := strconv.Itoa(int(page) + 1)
		resultLink += "</licenses/?devices=" + devices + "&page=" + nextPage + "&per_page=" + lsperpage + ">; rel=\"next\"; title=\"next\""
	}

	if page > 0 {
		previousPage := strconv.Itoa(int(page) - 1)
		if len(resultLink) > 0 {
			resultLink += ", "
		}
		resultLink += "</licenses/?devices=" + devices + "&page=" + previousPage + "&per_page=" + lsperpage + ">; rel=\"previous\"; title=\"previous\""
	}

	if len(resultLink) > 0 {
		w.Header().Set("Link", resultLink)
	}

	enc := json.NewEncoder(w)
	err = enc.Encode(licenseStatuses)
	if err != nil {
		problem.Error(w, r, problem.Problem{Detail: err.Error()}, http.StatusInternalServerError)
		return
	}
}

//ListRegisteredDevices returns data about the use of a given license
func ListRegisteredDevices(w http.ResponseWriter, r *http.Request, s Server) {
	w.Header().Set("Content-Type", api.ContentType_JSON)

	vars := mux.Vars(r)
	licenseFk := vars["key"]

	licenseStatus, err := s.LicenseStatuses().GetByLicenseId(licenseFk)
	if err != nil {
		if licenseStatus == nil {
			problem.NotFoundHandler(w, r)
			//logging.WriteToFile(complianceTestNumber, REGISTER_DEVICE, strconv.Itoa(http.StatusNotFound))
			return
		}

		problem.Error(w, r, problem.Problem{Detail: err.Error()}, http.StatusInternalServerError)
		return
	}

	registeredDevicesList := transactions.RegisteredDevicesList{Devices: make([]transactions.Device, 0), Id: licenseStatus.LicenseRef}

	fn := s.Transactions().ListRegisteredDevices(licenseStatus.Id)
	for it, err := fn(); err == nil; it, err = fn() {
		registeredDevicesList.Devices = append(registeredDevicesList.Devices, it)
	}

	enc := json.NewEncoder(w)
	err = enc.Encode(registeredDevicesList)
	if err != nil {
		problem.Error(w, r, problem.Problem{Detail: err.Error()}, http.StatusInternalServerError)
		return
	}
}

//CancelLicenseStatus cancel or revoke (according to the status) a license
func CancelLicenseStatus(w http.ResponseWriter, r *http.Request, s Server) {
	vars := mux.Vars(r)
	licenseFk := vars["key"]

	licenseStatus, err := s.LicenseStatuses().GetByLicenseId(licenseFk)

	if err != nil {
		if licenseStatus == nil {
			problem.NotFoundHandler(w, r)
			logging.WriteToFile(complianceTestNumber, CANCEL_REVOKE_LICENSE, strconv.Itoa(http.StatusNotFound))
			return
		}

		problem.Error(w, r, problem.Problem{Detail: err.Error()}, http.StatusInternalServerError)
		logging.WriteToFile(complianceTestNumber, CANCEL_REVOKE_LICENSE, strconv.Itoa(http.StatusInternalServerError))
		return
	}

	if licenseStatus.Status != status.STATUS_READY {
		problem.Error(w, r, problem.Problem{Detail: "The new status is not compatible with current status"}, http.StatusBadRequest)
		logging.WriteToFile(complianceTestNumber, CANCEL_REVOKE_LICENSE, strconv.Itoa(http.StatusBadRequest))
		return
	}

	var parsedLs licensestatuses.LicenseStatus
	err = decodeJsonLicenseStatus(r, &parsedLs)
	if err != nil {
		problem.Error(w, r, problem.Problem{Detail: err.Error()}, http.StatusInternalServerError)
		logging.WriteToFile(complianceTestNumber, CANCEL_REVOKE_LICENSE, strconv.Itoa(http.StatusInternalServerError))
		return
	}

	currentTime := time.Now()

	//update license using LCP Server
	httpStatusCode, errorr := updateLicense(currentTime, licenseFk)
	if errorr != nil {
		problem.Error(w, r, problem.Problem{Detail: errorr.Error()}, http.StatusInternalServerError)
		logging.WriteToFile(complianceTestNumber, CANCEL_REVOKE_LICENSE, strconv.Itoa(http.StatusInternalServerError))
		return
	}
	if httpStatusCode != http.StatusOK && httpStatusCode != http.StatusPartialContent { // 200, 206
		errorr = errors.New("LCP license PATCH returned HTTP error code " + strconv.Itoa(httpStatusCode))

		problem.Error(w, r, problem.Problem{Detail: errorr.Error()}, httpStatusCode)
		logging.WriteToFile(complianceTestNumber, CANCEL_REVOKE_LICENSE, strconv.Itoa(httpStatusCode))
		return
	}
	licenseStatus.CurrentEndLicense = &currentTime

	licenseStatus.Status = parsedLs.Status
	licenseStatus.Updated.Status = &currentTime
	licenseStatus.Updated.License = &currentTime

	err = s.LicenseStatuses().Update(*licenseStatus)
	if err != nil {
		problem.Error(w, r, problem.Problem{Detail: err.Error()}, http.StatusInternalServerError)
		logging.WriteToFile(complianceTestNumber, CANCEL_REVOKE_LICENSE, strconv.Itoa(http.StatusInternalServerError))
		return
	}

	logging.WriteToFile(complianceTestNumber, CANCEL_REVOKE_LICENSE, strconv.Itoa(http.StatusOK))
}

//makeLicenseStatus sets fields of license status according to the config file
//and creates needed inner objects of license status
func makeLicenseStatus(license license.License, ls *licensestatuses.LicenseStatus) {
	ls.LicenseRef = license.Id

	registerAvailable := config.Config.LicenseStatus.Register

	if license.Rights == nil || license.Rights.End == nil {
		// The publication was purchased (not a loan), so we do not set LSD.PotentialRights.End
		ls.CurrentEndLicense = nil
	} else {
		// license.Rights.End exists => this is a loan
		endFromLicense := license.Rights.End.Add(0)
		ls.CurrentEndLicense = &endFromLicense
		ls.PotentialRights = new(licensestatuses.PotentialRights)

		rentingDays := config.Config.LicenseStatus.RentingDays
		if rentingDays > 0 {
			endFromConfig := license.Issued.Add(time.Hour * 24 * time.Duration(rentingDays))

			if endFromLicense.After(endFromConfig) {
				ls.PotentialRights.End = &endFromLicense
			} else {
				ls.PotentialRights.End = &endFromConfig
			}
		} else {
			ls.PotentialRights.End = &endFromLicense
		}
	}

	if registerAvailable {
		ls.Status = status.STATUS_READY
	} else {
		ls.Status = status.STATUS_ACTIVE
	}

	ls.Updated = new(licensestatuses.Updated)
	ls.Updated.License = &license.Issued

	currentTime := time.Now()
	ls.Updated.Status = &currentTime

	count := 0
	ls.DeviceCount = &count
}

//getEvents gets the events from database for the license status
func getEvents(ls *licensestatuses.LicenseStatus, s Server) error {
	events := make([]transactions.Event, 0)

	fn := s.Transactions().GetByLicenseStatusId(ls.Id)
	var err error
	var event transactions.Event
	for event, err = fn(); err == nil; event, err = fn() {
		events = append(events, event)
	}

	if err == transactions.NotFound {
		ls.Events = events
		err = nil
	}

	return err
}

//makeLinks creates and adds links to the license status
func makeLinks(ls *licensestatuses.LicenseStatus) {
	lsdBaseUrl := config.Config.LsdServer.PublicBaseUrl
	licenseLinkUrl := config.Config.LsdServer.LicenseLinkUrl
	lcpBaseUrl := config.Config.LcpServer.PublicBaseUrl
	//frontendBaseUrl := config.Config.FrontendServer.PublicBaseUrl
	registerAvailable := config.Config.LicenseStatus.Register

	licenseHasRightsEnd := ls.CurrentEndLicense != nil && !(*ls.CurrentEndLicense).IsZero()
	returnAvailable := config.Config.LicenseStatus.Return && licenseHasRightsEnd
	renewAvailable := config.Config.LicenseStatus.Renew && licenseHasRightsEnd

	links := new([]licensestatuses.Link)

	if licenseLinkUrl != "" {
		licenseLinkUrl_ := strings.Replace(licenseLinkUrl, "{license_id}", ls.LicenseRef, -1)
		link := licensestatuses.Link{Href: licenseLinkUrl_, Rel: "license", Type: api.ContentType_LCP_JSON, Templated: false}
		*links = append(*links, link)
	} else {
		link := licensestatuses.Link{Href: lcpBaseUrl + "/licenses/" + ls.LicenseRef, Rel: "license", Type: api.ContentType_LCP_JSON, Templated: false}
		*links = append(*links, link)
	}

	if registerAvailable {
		link := licensestatuses.Link{Href: lsdBaseUrl + "/licenses/" + ls.LicenseRef + "/register{?id,name}", Rel: "register", Type: api.ContentType_LSD_JSON, Templated: true}
		*links = append(*links, link)
	}

	if returnAvailable {
		link := licensestatuses.Link{Href: lsdBaseUrl + "/licenses/" + ls.LicenseRef + "/return{?id,name}", Rel: "return", Type: api.ContentType_LSD_JSON, Templated: true}
		*links = append(*links, link)
	}

	if renewAvailable {
		link := licensestatuses.Link{Href: lsdBaseUrl + "/licenses/" + ls.LicenseRef + "/renew{?end,id,name}", Rel: "renew", Type: api.ContentType_LSD_JSON, Templated: true}
		*links = append(*links, link)
	}

	ls.Links = *links
}

//makeEvent creates an event and fill it
func makeEvent(status string, deviceName string, deviceId string, licenseStatusFk int) *transactions.Event {
	event := transactions.Event{}
	event.DeviceId = deviceId
	event.DeviceName = deviceName
	event.Timestamp = time.Now()
	event.Type = status
	event.LicenseStatusFk = licenseStatusFk

	return &event
}

//decodeJsonLicenseStatus decodes license status json to the object
func decodeJsonLicenseStatus(r *http.Request, ls *licensestatuses.LicenseStatus) error {
	var dec *json.Decoder

	if ctype := r.Header["Content-Type"]; len(ctype) > 0 && ctype[0] == api.ContentType_FORM_URL_ENCODED {
		buf := bytes.NewBufferString(r.PostFormValue("data"))
		dec = json.NewDecoder(buf)
	} else {
		dec = json.NewDecoder(r.Body)
	}

	err := dec.Decode(&ls)

	return err
}

//updateLicense updates license using LCP Server
func updateLicense(timeEnd time.Time, licenseRef string) (int, error) {

	lcpBaseUrl := config.Config.LcpServer.PublicBaseUrl
	if len(lcpBaseUrl) <= 0 {
		return 0, errors.New("Undefined Config.LcpServer.PublicBaseUrl")
	}

	l := license.License{Id: licenseRef, Rights: new(license.UserRights)}
	l.Rights.End = &timeEnd

	var lcpClient = &http.Client{
		Timeout: time.Second * 10,
	}
	pr, pw := io.Pipe()
	go func() {
		_ = json.NewEncoder(pw).Encode(l)
		pw.Close()
	}()
	req, err := http.NewRequest("PATCH", lcpBaseUrl+"/licenses/"+l.Id, pr)
	if err != nil {
		return 0, err
	}

	updateAuth := config.Config.LcpUpdateAuth

	if updateAuth.Username != "" {
		req.SetBasicAuth(updateAuth.Username, updateAuth.Password)
	}

	req.Header.Add("Content-Type", api.ContentType_LCP_JSON)
	response, err := lcpClient.Do(req)
	if err == nil {
		if response.StatusCode != http.StatusOK {
			log.Println("Notify Lcp Server of License (" + l.Id + ") = " + strconv.Itoa(response.StatusCode))
		}
		return response.StatusCode, nil
	}

	log.Println("Error Notify Lcp Server of License (" + l.Id + "):" + err.Error())
	return 0, err
}

//fillLicenseStatus fills object 'links' and field 'message' in license status
func fillLicenseStatus(ls *licensestatuses.LicenseStatus, r *http.Request, s Server) error {
	makeLinks(ls)

	acceptLanguages := r.Header.Get("Accept-Language")
	localization.LocalizeMessage(acceptLanguages, &ls.Message, ls.Status)

	err := getEvents(ls, s)

	return err
}
