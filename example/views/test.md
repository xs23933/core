### 商户基本信息

- [签名加密](/help/sign)
- 商户信息
- [收款订单](/help/payment)
- [付款订单](/help/disburse)

@baseurl = http://localhost:8080

### 查询商户信息

> 查询商户信息 `GET /v1/merchant`

| 参数名 | 类型   | 必填 | 说明                            |
| ------ | ------ | ---- | ------------------------------- |
| ts     | number | Y    | 时间戳 (秒为单位的 Unix 时间戳) |

### 请求示例

```http
### 查询商户信息

GET {{baseurl}}/v1/merchant?ts=1623945212 HTTP/1.1
Signature: 0c4326158df7444daf4ea1e29b01181c,f847401436f4851d3ea4dc85e733e8b6b8928701fa8044d6ed38a97f11f64934

###
```

### 返回示例

```json
{
  "code": 0,
  "data": {
    "id": "0c4326158df7444daf4ea1e29b01181c",
    "created_at": "2024-10-16T17:52:14.117493+08:00",
    "updated_at": "2024-10-25T11:26:57.33316+08:00",
    "name": "Example",
    "balance": 11,
    "total_balance": 111,
    "frozen_balance": 100,
    "owner_id": "8dfa4d4d75db4a1e806bcaade0984d54"
  }
}
```

| 参数名           | 类型   | 必返回 | 说明          |
| ---------------- | ------ | ------ | ------------- |
| code             | number | Y      | 状态码 0 成功 |
| msg              | string | N      | 返回信息      |
| data             | object | Y      | 返回数据      |
| - id             | string | Y      | 商户 ID       |
| - created_at     | string | Y      | 创建时间      |
| - updated_at     | string | Y      | 更新时间      |
| - name           | string | Y      | 商户名称      |
| - balance        | number | Y      | 可用余额      |
| - frozen_balance | number | Y      | 锁定余额      |
| - total_balance  | number | Y      | 总计余额      |
| - owner_id       | string | Y      | 商户拥有者 ID |
