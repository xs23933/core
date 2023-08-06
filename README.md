
# Core web framework v2.0
> Core is used for rapid development of enterprise application in Go, including RESTful APIs, web apps and backend services.

It is inspired by Tornado, Sinatra and Koa(node.js). Core has some Go-specific features such as interfaces and struct embedding.

* auto register router
* koa(node.js) develop style
* simple light framework


# example/work

> auto register router mode

```shell

example/work > 

     > handler > handler.go

     > models  > models.go
    
     > main.go
    
     > config.yaml

```

### handler.go file

```go
// define handler
type Handler struct {
	core.Handler // must extends core.Handler
}

// Put create user
// put /
//
// {
//     "user": "username",
//     "password": "password"
// }
// route: PUT / > main.Handler.Put
func (Handler) Put(c core.Ctx) {
	form := models.User{}
    // read put post body data
	if err := c.ReadBody(&form); err != nil {
		c.ToJSON(nil, err)
		return
	}
	c.ToJSON(form, form.Save())
}

// GetParam get some param
// 
// get param id planA
// @param: uid.UID userId
// route: GET /detail/:param > main.Handler.GetDetailParam
func (Handler) GetDetailParam(c core.Ctx) {
    // get param
	uid, err := c.ParamsUid("param")
	if err != nil {
		c.ToJSON(nil, err)
		return
	}
	c.ToJSON(models.UserById(uid))
}

// Get_id get id param
// 
// get param id planB
// @id: uid.UID userID
// route: GET /:id > main.Handler.Get_id
func (Handler) Get_id(c core.Ctx) {
    // get uri path id
	uid, err := c.ParamsUid("id")
	if err != nil {
		c.ToJSON(nil, err)
		return
	}
	c.ToJSON(models.UserById(uid))
}


// Get get user list
// get /
//
// route: GET / > main.Handler.Get
func (Handler) Get(c core.Ctx) {
	c.ToJSON(models.UserPage())
}

func init() {
    // auto register handler
	core.RegHandle(new(Handler))
}
```

### models.go file

```go
type User struct {
	core.Model
	User     string `json:"user" gorm:"size:32"`
	Password string `json:"password" gorm:"size:96"`
}

func (m *User) Save() error {
	return DB.Save(m).Error
}

func UserById(id uid.UID) (user User, err error) {
	err = DB.First(&user, "id = ?", id).Error
	return
}

func UserPage() (any, error) {
	result := make([]User, 0)
	err := core.Find(&result)
	return result, err
}

func InitDB() {
	DB = core.Conn()
	DB.AutoMigrate(&User{})
}

var (
	DB *core.DB
)

```

### main.go

```go
func main() {
	app := core.New(core.LoadConfigFile("config.yaml"))
	models.InitDB()
	app.Listen()
}
```

### config.yaml
```yaml
debug: true
network: tcp4
listen: 8080
restful:
    data: data
    status: success
    message: msg

prefork: false

database:
    type: sqlite3
    dsn: dat
```

# rust-client test file
```sh
@baseURL = http://localhost:8080
@contentType = application/json

### add user
PUT {{baseURL}} HTTP/1.1
Content-Type: {{contentType}}

{
    "user": "x",
    "password": "x"
}

### get user
GET {{baseURL}}/SYMFRZ4O65T9

### get user list
GET {{baseURL}}
```

## register user test

```sh
@baseURL = http://localhost:8080
@contentType = application/json

### add user
PUT {{baseURL}} HTTP/1.1
Content-Type: {{contentType}}

{
    "user": "x",
    "password": "x"
}
```

> Response

```h
HTTP/1.1 200 OK
Content-Type: application/json; charset=utf-8
X-Request-Id: ZO6I1Y8F0PTJ
Date: Sun, 06 Aug 2023 14:26:17 GMT
Content-Length: 166
Connection: close

{
  "data": {
    "id": "SYMFRZ4O65T9",
    "created_at": "0001-01-01T00:00:00Z",
    "updated_at": "2023-08-06T22:26:17.440943+08:00",
    "user": "x",
    "password": "x"
  },
  "msg": "ok",
  "success": true
}
```

## get user by id

```h
### get user
GET {{baseURL}}/SYMFRZ4O65T9
```

```h
HTTP/1.1 200 OK
Content-Type: application/json; charset=utf-8
X-Request-Id: 4M5J3HZA6LSK
Date: Sun, 06 Aug 2023 14:23:27 GMT
Content-Length: 178
Connection: close

{
  "data": {
    "id": "SYMFRZ4O65T9",
    "created_at": "2023-08-06T21:53:08.206575+08:00",
    "updated_at": "2023-08-06T22:21:18.325284+08:00",
    "user": "x",
    "password": "x"
  },
  "msg": "ok",
  "success": true
}
```

## get user list

```h
### get user list
GET {{baseURL}}
```

```h
HTTP/1.1 200 OK
Content-Type: application/json; charset=utf-8
X-Request-Id: BL6ALTKQTLZB
Date: Sun, 06 Aug 2023 14:24:01 GMT
Content-Length: 326
Connection: close

{
  "data": [
    {
      "id": "05HG0DIM522N",
      "created_at": "2023-08-06T21:49:34.735412+08:00",
      "updated_at": "2023-08-06T21:49:34.735412+08:00",
      "user": "xs",
      "password": "xs"
    },
    {
      "id": "SYMFRZ4O65T9",
      "created_at": "2023-08-06T21:53:08.206575+08:00",
      "updated_at": "2023-08-06T22:21:18.325284+08:00",
      "user": "x",
      "password": "x"
    }
  ],
  "msg": "ok",
  "success": true
}
```