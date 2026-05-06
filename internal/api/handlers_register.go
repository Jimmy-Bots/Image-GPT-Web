package api

import (
	"net/http"
	"time"
)

func (s *Server) handleGetRegisterState(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.register.Runtime())
}

func (s *Server) handleGetRegisterLogs(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": s.register.Logs()})
}

func (s *Server) handleSaveRegisterConfig(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	var payload map[string]any
	if err := decodeJSON(r, &payload); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	state, err := s.register.SaveConfig(r.Context(), payload)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "save_register_config_failed", err.Error())
		return
	}
	s.addLog(r, "account", "保存注册配置", map[string]any{"config": state.Config})
	writeJSON(w, http.StatusOK, map[string]any{"state": state})
}

func (s *Server) handleStartRegister(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	data, err := s.register.Start(r.Context())
	if err != nil {
		writeError(w, http.StatusBadRequest, "start_register_failed", err.Error())
		return
	}
	s.addLog(r, "account", "启动注册任务", map[string]any{"time": time.Now().UTC()})
	writeJSON(w, http.StatusOK, data)
}

func (s *Server) handleStopRegister(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	data := s.register.Stop()
	s.addLog(r, "account", "停止注册任务", map[string]any{"time": time.Now().UTC()})
	writeJSON(w, http.StatusOK, data)
}

func (s *Server) handleRunRegisterOnce(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	data, err := s.register.RunOnce(r.Context())
	if err != nil {
		writeError(w, http.StatusBadRequest, "register_once_failed", err.Error())
		return
	}
	s.addLog(r, "account", "执行单次注册", map[string]any{"time": time.Now().UTC()})
	writeJSON(w, http.StatusOK, data)
}
