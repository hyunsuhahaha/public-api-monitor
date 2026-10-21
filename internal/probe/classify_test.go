package probe

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRateLimitAndAuthEvidence(t *testing.T) {
	for _, c := range []struct {
		name   string
		obs    Observation
		status Status
		reason string
	}{
		{"per-second quota", Observation{HTTPStatus: 200, Body: []byte(`<resultCode>23</resultCode>`)}, StatusClient, "code 23"},
		{"daily quota inside 403", Observation{HTTPStatus: 403, Body: []byte(`<returnReasonCode>22</returnReasonCode>`)}, StatusClient, "code 22"},
		{"unknown 403", Observation{HTTPStatus: 403}, StatusClient, "원인 확인 필요"},
	} {
		t.Run(c.name, func(t *testing.T) {
			v := Classify(c.obs, 0)
			if v.Status != c.status || !strings.Contains(v.Reason, c.reason) {
				t.Fatalf("got %+v, want %s with %q", v, c.status, c.reason)
			}
		})
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		name   string
		obs    Observation
		status Status
		code   string
	}{
		{"json normal", Observation{HTTPStatus: 200, Body: []byte(`{"response":{"header":{"resultCode":"00","resultMsg":"NORMAL_SERVICE"},"body":{}}}`)}, StatusOK, "00"},
		{"xml normal", Observation{HTTPStatus: 200, Body: []byte(`<response><header><resultCode>00</resultCode><resultMsg>NORMAL SERVICE.</resultMsg></header></response>`)}, StatusOK, "00"},
		{"no data is still up", Observation{HTTPStatus: 200, Body: []byte(`{"response":{"header":{"resultCode":"03","resultMsg":"NO_DATA"}}}`)}, StatusOK, "03"},
		{"gateway: key not registered", Observation{HTTPStatus: 200, Body: []byte(`<OpenAPI_ServiceResponse><cmmMsgHeader><errMsg>SERVICE ERROR</errMsg><returnAuthMsg>SERVICE_KEY_IS_NOT_REGISTERED_ERROR</returnAuthMsg><returnReasonCode>30</returnReasonCode></cmmMsgHeader></OpenAPI_ServiceResponse>`)}, StatusClient, "30"},
		{"gateway: quota", Observation{HTTPStatus: 200, Body: []byte(`<OpenAPI_ServiceResponse><cmmMsgHeader><returnAuthMsg>LIMITED_NUMBER_OF_SERVICE_REQUESTS_EXCEEDS_ERROR</returnAuthMsg><returnReasonCode>22</returnReasonCode></cmmMsgHeader></OpenAPI_ServiceResponse>`)}, StatusClient, "22"},
		{"provider db error inside 200", Observation{HTTPStatus: 200, Body: []byte(`{"response":{"header":{"resultCode":"02","resultMsg":"DB_ERROR"}}}`)}, StatusError, "02"},
		{"provider timeout inside 200", Observation{HTTPStatus: 200, Body: []byte(`<response><header><resultCode>05</resultCode><resultMsg>SERVICETIMEOUT_ERROR</resultMsg></header></response>`)}, StatusError, "05"},
		{"numeric resultCode in json", Observation{HTTPStatus: 200, Body: []byte(`{"header":{"resultCode":0,"resultMsg":"ok"}}`)}, StatusOK, "0"},
		{"seoul ok", Observation{HTTPStatus: 200, Body: []byte(`{"rentBikeStatus":{"list_total_count":5,"RESULT":{"CODE":"INFO-000","MESSAGE":"정상 처리되었습니다."}}}`)}, StatusOK, "INFO-000"},
		{"seoul citydata ok", Observation{HTTPStatus: 200, Body: []byte(`{"list_total_count":1,"RESULT":{"RESULT.CODE":"INFO-000","RESULT.MESSAGE":"정상 처리되었습니다."},"CITYDATA":{}}`)}, StatusOK, "INFO-000"},
		{"gateway: service retired", Observation{HTTPStatus: 400, Body: []byte(`<OpenAPI_ServiceResponse><cmmMsgHeader><errMsg>NO_OPENAPI_SERVICE_ERROR</errMsg><returnAuthMsg>해당 오픈API 서비스가 없거나 폐기됨</returnAuthMsg><returnReasonCode>12</returnReasonCode></cmmMsgHeader></OpenAPI_ServiceResponse>`)}, StatusError, "12"},
		{"gateway: key not registered as 403 (2026 gateway)", Observation{HTTPStatus: 403, Body: []byte(`<OpenAPI_ServiceResponse><cmmMsgHeader><errMsg>SERVICE_KEY_IS_NOT_REGISTERED_ERROR</errMsg><returnReasonCode>30</returnReasonCode></cmmMsgHeader></OpenAPI_ServiceResponse>`)}, StatusClient, "30"},
		{"seoul bad key", Observation{HTTPStatus: 200, Body: []byte(`{"RESULT":{"CODE":"INFO-100","MESSAGE":"인증키가 유효하지 않습니다."}}`)}, StatusClient, "INFO-100"},
		{"seoul server error", Observation{HTTPStatus: 200, Body: []byte(`{"RESULT":{"CODE":"ERROR-500","MESSAGE":"서버 오류입니다."}}`)}, StatusError, "ERROR-500"},
		{"subway ok", Observation{HTTPStatus: 200, Body: []byte(`{"errorMessage":{"status":200,"code":"INFO-000","message":"정상 처리되었습니다."},"realtimeArrivalList":[]}`)}, StatusOK, "INFO-000"},
		{"seoul bus ok", Observation{HTTPStatus: 200, Body: []byte(`<ServiceResult><msgHeader><headerCd>0</headerCd><headerMsg>정상적으로 처리되었습니다.</headerMsg></msgHeader></ServiceResult>`)}, StatusOK, "BUS-0"},
		{"seoul bus key", Observation{HTTPStatus: 200, Body: []byte(`<ServiceResult><msgHeader><headerCd>7</headerCd><headerMsg>인증실패</headerMsg></msgHeader></ServiceResult>`)}, StatusClient, "BUS-7"},
		{"seoul bus system error", Observation{HTTPStatus: 200, Body: []byte(`<ServiceResult><msgHeader><headerCd>1</headerCd><headerMsg>시스템 에러</headerMsg></msgHeader></ServiceResult>`)}, StatusError, "BUS-1"},
		{"html error page with 200", Observation{HTTPStatus: 200, Body: []byte(`<!DOCTYPE html><html><body>Service Unavailable</body></html>`)}, StatusError, ""},
		{"plain unexpected errors", Observation{HTTPStatus: 200, Body: []byte(`Unexpected errors`)}, StatusError, ""},
		{"empty body", Observation{HTTPStatus: 200}, StatusError, ""},
		{"500", Observation{HTTPStatus: 500, Body: []byte(`oops`)}, StatusDown, ""},
		{"502 with header", Observation{HTTPStatus: 502, Body: []byte(`<resultCode>99</resultCode>`)}, StatusDown, "99"},
		{"404 endpoint gone", Observation{HTTPStatus: 404}, StatusError, ""},
		{"403 is our side", Observation{HTTPStatus: 403}, StatusClient, ""},
		{"timeout", Observation{Timeout: true, Err: errors.New("context deadline exceeded")}, StatusDown, ""},
		{"connection refused", Observation{Err: errors.New("dial tcp: connection refused")}, StatusDown, ""},
		{"slow but correct", Observation{HTTPStatus: 200, Latency: 4 * time.Second, Body: []byte(`<resultCode>00</resultCode>`)}, StatusSlow, "00"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := Classify(c.obs, 3*time.Second)
			if v.Status != c.status || v.Code != c.code {
				t.Fatalf("got %s/%q (%s), want %s/%q", v.Status, v.Code, v.Reason, c.status, c.code)
			}
		})
	}
}

func TestStatusHelpers(t *testing.T) {
	if StatusClient.Counts() || !StatusDown.Counts() {
		t.Fatal("client results must not count toward uptime")
	}
	if !StatusSlow.Healthy() || StatusError.Healthy() {
		t.Fatal("slow is up (degraded), error is not")
	}
	if StatusStale.Severity() != 1 || StatusDown.Severity() != 2 || StatusOK.Severity() != 0 {
		t.Fatal("severity order")
	}
}
