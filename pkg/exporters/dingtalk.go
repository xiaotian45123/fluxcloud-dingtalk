package exporters

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/justinbarrick/fluxcloud/pkg/config"
	"github.com/justinbarrick/fluxcloud/pkg/msg"
	"github.com/weaveworks/flux/event"
	"io/ioutil"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// The DingTalk exporter sends Flux events to a Microsoft Teams channel via a webhook.
type DingTalk struct {
	Access string `json:"access"`
	Secret string `json:"secret"`
	AtNum string `json:"at_num"`
}

// DingTalkMessage struct
type DingTalkMessage struct {
	Msgtype string `json:"msgtype"`
	Markdown struct {
		Title string `json:"title"`
		Text  string `json:"text"`
	} `json:"markdown"`
	At struct {
		AtMobiles []string `json:"atMobiles"`
		AtUserIds []string `json:"atUserIds "`
		IsAtAll   bool     `json:"isAtAll"`
	} `json:"at"`
}

// NewDingTalk Initialize a new DingTalk instance
func NewDingTalk(config config.Config) (*DingTalk, error) {
	var err error
	d := DingTalk{}
	d.Access, err = config.Required("dingtalk_access")
	if err != nil {
		return nil, err
	}
	d.Secret, err = config.Required("dingtalk_secret")
	if err != nil {
		return nil, err
	}
	d.AtNum, err = config.Required("dingtalk_at_num")

	if err != nil {
		return nil, err
	}
	fmt.Println(d)
	return &d, nil
}

// Send a DingTalkMessage to MS Teams
func (d *DingTalk) sign(t int64) string {
	payload := fmt.Sprintf("%d\n%s", t, d.Secret)
	h := hmac.New(sha256.New, []byte(d.Secret))
	h.Write([]byte(payload))
	data := h.Sum(nil)
	return base64.StdEncoding.EncodeToString(data)
}

func (d *DingTalk) Send(c context.Context, client *http.Client, message msg.Message) error {

	params := url.Values{}
	params.Set("access_token", d.Access)
	if d.Secret != "" { // 如果设置密钥,则签名
		t := time.Now().Unix() * 1000
		params.Set("timestamp", strconv.FormatInt(t, 10))
		params.Set("sign", d.sign(t))
	}

	dingMessage := d.NewDingTalkMessage(message)
	if dingMessage == nil {
		return nil
	}

	buf, err := json.Marshal(dingMessage)
	if err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodPost, "https://oapi.dingtalk.com/robot/send", bytes.NewBuffer(buf))
	if err != nil {
		return err
	}
	req.URL.RawQuery = params.Encode()
	req.Header.Add("Content-Type", "application/json;charset=utf-8")

	reqt, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Println("get url failed ,err: ", err)
		return nil
	}
	defer reqt.Body.Close() //关闭网络连接
	resp, err := ioutil.ReadAll(reqt.Body)
	if err != nil {
		fmt.Println("get url failed ,err: ", err)
		return nil
	}
	fmt.Println(string(resp))
	return nil
}

// NewLine Return the new line character for MS Teams messages
func (d *DingTalk) NewLine() string {
	return "\n"
}

// FormatLink Return a formatted link for MS Teams.
func (d *DingTalk) FormatLink(link string, name string) string {
	return fmt.Sprintf("[%s](%s)", name, link)
}

// NewDingTalkMessage Convert a flux event into a MS Teams message
func (d *DingTalk) NewDingTalkMessage(message msg.Message) *DingTalkMessage {
	autoReleaseMessage := new(DingTalkMessage)
	var msgType string = message.Event.Metadata.Type()
	var msgMetadta event.EventMetadata = message.Event.Metadata
	fmt.Printf("msgType is: %v \n", msgType)
	//时间需要继续处理为本地时间   环境可以作为变量传递进来
	var f1 = func(t time.Time) string {
		cst, _ := time.LoadLocation("Asia/Shanghai")
		localStopTime := t.In(cst).Format("2006-01-02 15:04:05")
		return localStopTime
	}
	startTime := f1(message.Event.StartedAt)
	endTime := f1(message.Event.EndedAt)
	switch msgType {
	case event.EventAutoRelease:
		newImage := msgMetadta.(*event.AutoReleaseEventMetadata).Spec.Changes[0].ImageID.String()
		oldImage := msgMetadta.(*event.AutoReleaseEventMetadata).Spec.Changes[0].Container.Image.String()

		fmt.Println(newImage, oldImage)
		var workloadArr []string
		for _, v := range message.Event.ServiceIDs {
			workloadArr = append(workloadArr, v.String())
		}

		appStr := fmt.Sprintf(strings.Join(workloadArr, "\n\n"))
		autoReleaseMessage.Msgtype = "markdown"
		autoReleaseMessage.Markdown.Title = "EventAutoRelease"
		autoReleaseMessage.Markdown.Text = fmt.Sprintf(
			"<font color=#008000 size=5 >上线通知 </font>  \n\n **开始时间**：%v \n\n **结束时间**：%v  \n\n **上线应用**：\n\n %v "+
				"\n\n **新镜像**：%v \n\n **旧镜像**: %v",
			startTime, endTime, appStr, newImage, oldImage,
		)
		return autoReleaseMessage
	case event.EventSync:
		var errArr []string
		var errStr string
		if len(msgMetadta.(*event.SyncEventMetadata).Errors) > 0 {
			for _, v := range msgMetadta.(*event.SyncEventMetadata).Errors {
				errArr = append(errArr, v.Error)
			}


			//处理at人员
			/*	AtNum处理逻辑：
				去掉接受进来的字符串的首尾空格，赋值给nonTrimSpaceAtNum
				1： 判断nonTrimSpaceAtNum是否为空，true 就不需要@任何人
				2： 判断nonTrimSpaceAtNum是all,就@所有人
				3： （1，2不成立），就证明只@部分人。校验数据有all错误提示，因为手机号里面不可能有all。
				    继续判断，手机号之间连续超过2个字符串会被当作元素，出现@" "的情况，异常。
			*/
			//由于不影响主流程。@格式错误只提示，不中断流程

			//先去掉头尾空格，然后进行分割
			var atArr []string
			var atStr = ""
			var nonTrimSpaceAtNum = strings.TrimSpace(d.AtNum)

			if nonTrimSpaceAtNum == "" {
				fmt.Println("sync错误信息不需要@任何人")
			} else if strings.ToUpper(nonTrimSpaceAtNum) == "ALL" {
				fmt.Println("sync错误信息@所有人")
				autoReleaseMessage.At.IsAtAll = true
			} else {
				// 校验输入的手机号里面是否是混合[123 all]这种格式错误
				if strings.Contains(strings.ToUpper(d.AtNum), "ALL") {
					fmt.Println("AtNum配置错误,all只能单独使用 ")
				} else if !strings.Contains(strings.ToUpper(d.AtNum), "ALL",) && nonTrimSpaceAtNum != "" && strings.Contains(nonTrimSpaceAtNum, "  ") {
					fmt.Println("手机号之间只能单个空格隔开")
				} else if !strings.Contains(
					strings.ToUpper(d.AtNum), "ALL",
				) && nonTrimSpaceAtNum != "" && !strings.Contains(nonTrimSpaceAtNum, "  ") {
					atArr = strings.Split(strings.TrimSpace(d.AtNum), " ")
					atStr = "@" + strings.Join(atArr, " @")
					autoReleaseMessage.At.AtMobiles = atArr

				}
			}
			//处理@人员完毕
			errStr = strings.Join(errArr, "\n\n")
			autoReleaseMessage.Msgtype = "markdown"
			autoReleaseMessage.Markdown.Title = "Sync errors"
			autoReleaseMessage.Markdown.Text = fmt.Sprintf(
				"<font color=#FF0000 size=5 >同步错误，开发忽略！！！ </font>    \n\n **错误信息**：%v \n\n %v ", errStr,atStr)
			return autoReleaseMessage
		}
	}
	return nil
}

// Name Return the name of the exporter.
func (d *DingTalk) Name() string {
	return "DingTalk"
}
