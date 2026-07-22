package main
import (
  "bytes"; "fmt"; "io"; "net/http"; "os"; "time"
  "github.com/google/uuid"
  "github.com/owlspeak/owl-server/backend/internal/security"
  "github.com/owlspeak/owl-server/backend/internal/model"
  "gorm.io/driver/postgres"
  "gorm.io/gorm"
  "gorm.io/gorm/logger"
)
func token(uid string) string {
  secret := os.Getenv("JWT_SECRET")
  if secret == "" { secret = "replace-this-with-at-least-32-random-characters" }
  tm := security.NewTokenManager(secret, time.Hour)
  tok,_,err := tm.AccessTokenWithAudience(uuid.MustParse(uid), security.AudienceClient)
  if err != nil { panic(err) }
  return tok
}
func do(label, method, url, body, tok string) {
  var r io.Reader
  if body != "" { r = bytes.NewBufferString(body) }
  req,_ := http.NewRequest(method, url, r)
  req.Header.Set("Authorization", "Bearer "+tok)
  if body != "" { req.Header.Set("Content-Type", "application/json") }
  resp, err := http.DefaultClient.Do(req)
  if err != nil { fmt.Println(label, "ERR", err); return }
  b,_ := io.ReadAll(resp.Body); resp.Body.Close()
  out := string(b); if len(out)>200 { out = out[:200]+"..." }
  fmt.Printf("%s -> %d %s\n", label, resp.StatusCode, out)
}
func main() {
  dsn := os.Getenv("DATABASE_URL")
  if dsn=="" { dsn = "postgres://owl:owl_dev_password@127.0.0.1:5432/owl?sslmode=disable" }
  db,_ := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
  me := uuid.MustParse("fa98c2b5-72cd-4ff7-ba01-5de46ef4090f")
  target := uuid.MustParse("503a199d-28c8-4fa2-ba6c-62c0eb4f1406")
  // clear
  db.Where("(user_id=? AND target_user_id=?) OR (user_id=? AND target_user_id=?)", me, target, target, me).Delete(&model.Relationship{})
  // case1: no prior relationship
  do("block-stranger", "PUT", "http://127.0.0.1:8080/gapi/v1/users/@me/relationships/"+target.String(), `{"type":"blocked"}`, token(me.String()))
  // case2: already blocked (idempotent)
  do("block-again", "PUT", "http://127.0.0.1:8080/gapi/v1/users/@me/relationships/"+target.String(), `{"type":"blocked"}`, token(me.String()))
  // case3: pending outgoing then block
  db.Where("(user_id=? AND target_user_id=?) OR (user_id=? AND target_user_id=?)", me, target, target, me).Delete(&model.Relationship{})
  now := time.Now().UTC()
  db.Create(&model.Relationship{ID: uuid.New(), UserID: me, TargetUserID: target, Type: model.RelationshipPendingOutgoing, CreatedAt: now, UpdatedAt: now})
  do("block-pending-out", "PUT", "http://127.0.0.1:8080/gapi/v1/users/@me/relationships/"+target.String(), `{"type":"blocked"}`, token(me.String()))
  // case4: pending incoming then block
  db.Where("(user_id=? AND target_user_id=?) OR (user_id=? AND target_user_id=?)", me, target, target, me).Delete(&model.Relationship{})
  db.Create(&model.Relationship{ID: uuid.New(), UserID: target, TargetUserID: me, Type: model.RelationshipPendingOutgoing, CreatedAt: now, UpdatedAt: now})
  do("block-pending-in", "PUT", "http://127.0.0.1:8080/gapi/v1/users/@me/relationships/"+target.String(), `{"type":"blocked"}`, token(me.String()))
  // case5: friends then block
  db.Where("(user_id=? AND target_user_id=?) OR (user_id=? AND target_user_id=?)", me, target, target, me).Delete(&model.Relationship{})
  db.Create(&[]model.Relationship{
    {ID: uuid.New(), UserID: me, TargetUserID: target, Type: model.RelationshipFriend, CreatedAt: now, UpdatedAt: now},
    {ID: uuid.New(), UserID: target, TargetUserID: me, Type: model.RelationshipFriend, CreatedAt: now, UpdatedAt: now},
  })
  do("block-friend", "PUT", "http://127.0.0.1:8080/gapi/v1/users/@me/relationships/"+target.String(), `{"type":"blocked"}`, token(me.String()))
  // verify
  var rows []model.Relationship
  db.Where("user_id=? OR user_id=?", me, target).Find(&rows)
  for _, r := range rows {
    fmt.Printf("row %s -> %s type=%s\n", r.UserID, r.TargetUserID, r.Type)
  }
}
