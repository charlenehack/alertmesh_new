package router

import (
	"errors"
	"strconv"
	"strings"
	"time"

	restful "github.com/emicklei/go-restful/v3"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"

	ai_pkg "github.com/kuzane/alertmesh/internal/ai"
	"github.com/kuzane/alertmesh/internal/config"
	"github.com/kuzane/alertmesh/internal/httputil"
	"github.com/kuzane/alertmesh/internal/label"
	"github.com/kuzane/alertmesh/internal/model"
)

type aiReportHandler struct {
	db  *gorm.DB
	cfg *config.Config
}

func newAIReportHandler(db *gorm.DB, cfg *config.Config) *aiReportHandler {
	return &aiReportHandler{db: db, cfg: cfg}
}

func (h *aiReportHandler) registerRoutes(ws *restful.WebService) {
	ws.Route(ws.GET("/ai-reports").
		To(h.list).
		Doc("List periodic AI reports").
		Metadata(label.MetaAuth, label.Enable).
		Metadata(label.MetaACL, label.Enable))

	ws.Route(ws.POST("/ai-reports").
		To(h.create).
		Doc("Generate a new periodic AI report").
		Metadata(label.MetaAuth, label.Enable).
		Metadata(label.MetaACL, label.Enable))

	ws.Route(ws.GET("/ai-reports/{id}").
		To(h.getOne).
		Doc("Get a periodic AI report by ID").
		Metadata(label.MetaAuth, label.Enable).
		Metadata(label.MetaACL, label.Enable))
}

func (h *aiReportHandler) list(req *restful.Request, resp *restful.Response) {
	offset, _ := strconv.Atoi(req.QueryParameter("offset"))
	limit, _ := strconv.Atoi(req.QueryParameter("limit"))
	if limit <= 0 {
		limit = 20
	}

	var reports []model.AIReport
	var total int64

	q := h.db.WithContext(req.Request.Context()).Model(&model.AIReport{})
	q.Count(&total)
	if err := q.Order("start_time DESC").Offset(offset).Limit(limit).Find(&reports).Error; err != nil {
		httputil.InternalError(resp, err.Error())
		return
	}

	httputil.Success(resp, map[string]any{
		"items": reports,
		"total": total,
	})
}

type createAIReportRequest struct {
	Period    string `json:"period"`
	StartTime string `json:"start_time"`
	EndTime   string `json:"end_time"`
}

func (h *aiReportHandler) create(req *restful.Request, resp *restful.Response) {
	var body createAIReportRequest
	if err := req.ReadEntity(&body); err != nil {
		httputil.BadRequest(resp, "invalid request body")
		return
	}

	body.Period = strings.TrimSpace(body.Period)
	if body.Period != "day" && body.Period != "week" && body.Period != "month" {
		httputil.BadRequest(resp, "period must be one of: day, week, month")
		return
	}

	startTime, err := time.Parse(time.RFC3339, body.StartTime)
	if err != nil {
		httputil.BadRequest(resp, "invalid start_time: "+err.Error())
		return
	}
	endTime, err := time.Parse(time.RFC3339, body.EndTime)
	if err != nil {
		httputil.BadRequest(resp, "invalid end_time: "+err.Error())
		return
	}
	if !endTime.After(startTime) {
		httputil.BadRequest(resp, "end_time must be after start_time")
		return
	}

	report := &model.AIReport{
		Period:    body.Period,
		StartTime: startTime,
		EndTime:   endTime,
		Status:    model.AIReportStatusRunning,
	}
	if err := h.db.WithContext(req.Request.Context()).Create(report).Error; err != nil {
		httputil.InternalError(resp, err.Error())
		return
	}

	ctx := req.Request.Context()
	var fullReport strings.Builder
	agent := ai_pkg.NewAgent(h.db, h.cfg)
	genErr := agent.AnalyzePeriod(ctx, ai_pkg.PeriodReportInput{
		Period:    body.Period,
		StartTime: startTime,
		EndTime:   endTime,
	}, func(token string) {
		fullReport.WriteString(token)
	})

	if genErr != nil {
		log.Error().Err(genErr).Str("report_id", report.ID).Msg("AI period report generation failed")
		h.db.Model(report).Updates(map[string]any{
			"status": model.AIReportStatusFailed,
			"error":  genErr.Error(),
		})
		httputil.InternalError(resp, "AI generation failed: "+genErr.Error())
		return
	}

	h.db.Model(report).Updates(map[string]any{
		"status": model.AIReportStatusDone,
		"report": fullReport.String(),
	})

	report.Status = model.AIReportStatusDone
	report.Report = fullReport.String()
	httputil.Success(resp, report)
}

func (h *aiReportHandler) getOne(req *restful.Request, resp *restful.Response) {
	id := req.PathParameter("id")
	var report model.AIReport
	if err := h.db.WithContext(req.Request.Context()).First(&report, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			httputil.NotFound(resp)
			return
		}
		httputil.InternalError(resp, err.Error())
		return
	}
	httputil.Success(resp, report)
}
