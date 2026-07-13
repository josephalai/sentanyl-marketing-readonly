package routes

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gopkg.in/mgo.v2"
	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/pkg/db"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
)

// RegisterTodoRoutes mounts the tenant to-do API. The caller must wrap the
// group in RequireTenantAuth.
func RegisterTodoRoutes(rg *gin.RouterGroup) {
	rg.GET("/todos", handleListTodos)
	rg.POST("/todos", handleCreateTodo)
	rg.PUT("/todos/:id", handleUpdateTodo)
	rg.DELETE("/todos/:id", handleDeleteTodo)
}

// EnsureTodoIndexes creates the todos list index. Safe to call at startup.
func EnsureTodoIndexes() {
	_ = db.GetCollection(pkgmodels.TodoCollection).EnsureIndex(mgo.Index{
		Key: []string{"tenant_id", "status", "-created_at"}, Background: true,
	})
}

func handleListTodos(c *gin.Context) {
	tenantID, ok := tenantIDFromContext(c)
	if !ok {
		return
	}
	query := bson.M{"tenant_id": tenantID, "timestamps.deleted_at": nil}
	if status := c.Query("status"); status != "" {
		query["status"] = status
	}
	var todos []pkgmodels.Todo
	if err := db.GetCollection(pkgmodels.TodoCollection).Find(query).Sort("-timestamps.created_at").Limit(500).All(&todos); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list todos"})
		return
	}
	if todos == nil {
		todos = []pkgmodels.Todo{}
	}
	c.JSON(http.StatusOK, gin.H{"todos": todos})
}

func handleCreateTodo(c *gin.Context) {
	tenantID, ok := tenantIDFromContext(c)
	if !ok {
		return
	}
	var req struct {
		Title     string `json:"title" binding:"required"`
		Note      string `json:"note"`
		CreatedBy string `json:"created_by"`
		ContactID string `json:"contact_id"`
		ThreadID  string `json:"thread_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	todo := pkgmodels.NewTodo(tenantID, strings.TrimSpace(req.Title))
	todo.Note = req.Note
	if req.CreatedBy == pkgmodels.TodoCreatedByAI {
		todo.CreatedBy = pkgmodels.TodoCreatedByAI
	}
	if bson.IsObjectIdHex(req.ContactID) {
		todo.ContactID = bson.ObjectIdHex(req.ContactID)
	}
	if bson.IsObjectIdHex(req.ThreadID) {
		todo.EmailThreadID = bson.ObjectIdHex(req.ThreadID)
	}
	todo.SetCreated()
	if err := db.GetCollection(pkgmodels.TodoCollection).Insert(todo); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create todo"})
		return
	}
	c.JSON(http.StatusCreated, gin.H{"todo": todo})
}

func handleUpdateTodo(c *gin.Context) {
	tenantID, ok := tenantIDFromContext(c)
	if !ok {
		return
	}
	var todo pkgmodels.Todo
	if err := findByIDOrPublic(pkgmodels.TodoCollection, tenantID, c.Param("id"), &todo); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "todo not found"})
		return
	}
	var req struct {
		Title  *string `json:"title"`
		Note   *string `json:"note"`
		Status *string `json:"status"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	update := bson.M{"timestamps.updated_at": time.Now()}
	if req.Title != nil && strings.TrimSpace(*req.Title) != "" {
		update["title"] = strings.TrimSpace(*req.Title)
	}
	if req.Note != nil {
		update["note"] = *req.Note
	}
	if req.Status != nil {
		if *req.Status != pkgmodels.TodoStatusOpen && *req.Status != pkgmodels.TodoStatusDone {
			c.JSON(http.StatusBadRequest, gin.H{"error": "status must be open or done"})
			return
		}
		update["status"] = *req.Status
	}
	if err := db.GetCollection(pkgmodels.TodoCollection).UpdateId(todo.Id, bson.M{"$set": update}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update todo"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"updated": true})
}

func handleDeleteTodo(c *gin.Context) {
	tenantID, ok := tenantIDFromContext(c)
	if !ok {
		return
	}
	var todo pkgmodels.Todo
	if err := findByIDOrPublic(pkgmodels.TodoCollection, tenantID, c.Param("id"), &todo); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "todo not found"})
		return
	}
	if err := db.GetCollection(pkgmodels.TodoCollection).UpdateId(todo.Id, bson.M{"$set": bson.M{"timestamps.deleted_at": time.Now()}}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete todo"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": true})
}
