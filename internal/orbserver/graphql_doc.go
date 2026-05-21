package orbserver

// graphqlProxy is a documentation stub for the GraphQL proxy endpoint.
// The actual handler is handler.GraphQL.Handle from internal/handler.
//
// @Summary     GraphQL endpoint
// @Description POST: proxies GraphQL queries to orb's local DGraph instance. GET: serves the GraphiQL explorer UI.
// @Tags        graphql
// @Accept      json
// @Produce     json
// @Param       body body string true "GraphQL request body" example("{\"query\": \"{ queryServer { id hostname } }\"}")
// @Success     200 {object} map[string]interface{}
// @Router      /graphql [post]
func (s *Server) graphqlProxy() {}
