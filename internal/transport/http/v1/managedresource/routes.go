package managedresource

import "github.com/labstack/echo/v4"

// RegisterRoutes mounts /managed on the authenticated v1 group. Access is
// decided per call by the service from the caller's principal.
func RegisterRoutes(v1 *echo.Group, h *Handler) {
	g := v1.Group("/managed")
	g.GET("/hosts", h.ListHosts)
	g.POST("/hosts", h.CreateHost)
	g.POST("/hosts/batch", h.CreateHostsBatch)
	g.GET("/hosts/:id", h.GetHost)
	g.PUT("/hosts/:id", h.UpdateHost)
	g.DELETE("/hosts/:id", h.DeleteHost)
	g.PATCH("/hosts/:id/labels", h.SetHostLabels)
	g.POST("/hosts/:id/trust", h.TrustHostKeys)
	g.POST("/hosts/:id/check", h.CheckHost)
	g.GET("/access-profiles", h.ListProfiles)
	g.POST("/access-profiles", h.CreateProfile)
	g.GET("/access-profiles/:id", h.GetProfile)
	g.PUT("/access-profiles/:id", h.UpdateProfile)
	g.DELETE("/access-profiles/:id", h.DeleteProfile)
	g.GET("/operations", h.ListOperations)
	g.POST("/operations", h.SubmitOperation)
	g.POST("/operations/preview", h.PreviewOperation)
	g.GET("/operations/:id", h.GetOperation)
	g.POST("/operations/:id/cancel", h.CancelOperation)
	g.GET("/operations/:id/targets/:hostId/output", h.TargetOutput)
}
