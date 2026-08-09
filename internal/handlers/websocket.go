package handlers

import (
	"github.com/fasthttp/websocket"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/middleware"
	ws "github.com/shridarpatil/whatomate/internal/websocket"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

func newUpgrader(allowedOrigins map[string]bool) websocket.FastHTTPUpgrader {
	return websocket.FastHTTPUpgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		CheckOrigin: func(ctx *fasthttp.RequestCtx) bool {
			origin := string(ctx.Request.Header.Peek("Origin"))
			return middleware.IsOriginAllowed(origin, allowedOrigins)
		},
	}
}

func (a *App) wsUpgrader() websocket.FastHTTPUpgrader {
	allowedOrigins := middleware.ParseAllowedOrigins(a.Config.Server.AllowedOrigins)
	return newUpgrader(allowedOrigins)
}

// WebSocketHandler handles authenticated realtime connections. Native clients
// additionally register/unregister FCM tokens over this same authenticated
// channel; user/org ownership therefore comes only from validated JWT claims.
func (a *App) WebSocketHandler(r *fastglue.Request) error {
	ws.EnsureMobilePush(a.DB, a.Log)
	up := a.wsUpgrader()
	err := up.Upgrade(r.RequestCtx, func(conn *websocket.Conn) {
		registerPush := ws.RegisterPushFn(func(userID, orgID uuid.UUID, payload ws.PushRegistrationPayload) error {
			return ws.RegisterMobileDevice(a.DB, a.Log, userID, orgID, payload)
		})
		unregisterPush := ws.UnregisterPushFn(func(userID, orgID uuid.UUID, token string) error {
			return ws.UnregisterMobileDevice(a.DB, token, userID, orgID)
		})
		client := ws.NewUnauthenticatedClient(
			a.WSHub,
			conn,
			a.validateWSTokenFn(),
			registerPush,
			unregisterPush,
		)
		go client.WritePump()
		client.ReadPump()
	})

	if err != nil {
		a.Log.Error("WebSocket upgrade failed", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "WebSocket upgrade failed", nil, "")
	}
	return nil
}

func (a *App) validateWSTokenFn() ws.AuthenticateFn {
	return func(tokenString string) (uuid.UUID, uuid.UUID, error) {
		token, err := jwt.ParseWithClaims(tokenString, &middleware.JWTClaims{}, func(token *jwt.Token) (any, error) {
			return []byte(a.Config.JWT.Secret), nil
		})
		if err != nil || !token.Valid {
			return uuid.Nil, uuid.Nil, err
		}
		claims, ok := token.Claims.(*middleware.JWTClaims)
		if !ok {
			return uuid.Nil, uuid.Nil, jwt.ErrTokenInvalidClaims
		}
		return claims.UserID, claims.OrganizationID, nil
	}
}
