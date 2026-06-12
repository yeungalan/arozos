package notification

import (
	"container/list"

	"imuslab.com/arozos/mod/info/logger"
)

/*
	Notification Producer and Consumer Queue

	This module is designed to route the notification from module that produce it
	to all the devices or agent that can reach the user
*/

// Built-in notification agent identifiers. Use these constants instead of the
// raw strings so producers and agents stay in sync.
const (
	AgentWebSocket = "websocket" //Real-time delivery to connected desktop sessions
	AgentSMTP      = "smtpn"     //Email delivery via SMTP
)

type NotificationPayload struct {
	ID            string   //Notification ID, generate by producer
	Title         string   //Title of the notification
	Message       string   //Message of the notification
	Icon          string   //Optional Semantic UI icon class for desktop notification (e.g. "info circle")
	Receiver      []string //Receiver, username in arozos system
	Sender        string   //Sender, the sender or module of the notification
	ReciverAgents []string //Agent name that have access to this notification
}

type AgentProducerFunction func(*NotificationPayload) error

type Agent interface {
	//Defination of the agent
	Name() string     //The name of the notification agent, must be unique
	Desc() string     //Basic description of the agent
	IsConsumer() bool //Can receive notification can arozos core
	IsProducer() bool //Can produce notification to arozos core
	//ConsumerNotification is the endpoint for arozos -> this agent. It returns
	//(delivered, error) where delivered reports whether the notification actually
	//reached the intended recipient via this agent (e.g. the user was online for
	//the desktop agent, or an email was sent). The queue uses this to decide
	//whether to fall back to the next agent.
	ConsumerNotification(*NotificationPayload) (bool, error)
	ProduceNotification(*AgentProducerFunction) //Endpoint for this agent -> arozos
}

type NotificationQueue struct {
	Agents      []*Agent
	MasterQueue *list.List
}

func NewNotificationQueue() *NotificationQueue {
	thisQueue := list.New()

	return &NotificationQueue{
		Agents:      []*Agent{},
		MasterQueue: thisQueue,
	}
}

// Add a notification agent to the queue
func (q *NotificationQueue) RegisterNotificationAgent(agent Agent) {
	q.Agents = append(q.Agents, &agent)
}

// BroadcastNotification routes a notification to the registered consumer agents
// in their registration order, which acts as the delivery priority. The first
// agent that successfully delivers the message to the recipient stops the chain.
// This means a notification is shown on the user's connected desktop when they
// are online, and only falls back to email when they are not.
//
// message.ReciverAgents is an optional allow-list: when non-empty, only agents
// whose name appears in it are considered (their relative order is preserved).
// When empty, every registered consumer agent is eligible.
func (q *NotificationQueue) BroadcastNotification(message *NotificationPayload) error {
	delivered := false
	for _, agent := range q.Agents {
		thisAgent := *agent
		if !thisAgent.IsConsumer() {
			//This agent cannot receive notifications
			continue
		}

		//Honor the optional agent allow-list
		if len(message.ReciverAgents) > 0 && !stringInSlice(thisAgent.Name(), message.ReciverAgents) {
			continue
		}

		//Attempt to deliver this notification via this agent
		ok, err := thisAgent.ConsumerNotification(message)
		if err != nil {
			logger.PrintAndLog("Notification", "[Notification] Unable to send message via notification agent: "+thisAgent.Name(), err)
			//Fall through to the next agent in order
			continue
		}

		if ok {
			//Delivered by the highest-priority available agent; stop here
			delivered = true
			break
		}
		//Not delivered (e.g. user offline) — fall through to the next agent
	}

	if delivered {
		logger.PrintAndLog("Notification", "[Notification] Message titled: "+message.Title+" (ID: "+message.ID+") delivered", nil)
	} else {
		logger.PrintAndLog("Notification", "[Notification] Message titled: "+message.Title+" (ID: "+message.ID+") could not be delivered by any agent", nil)
	}
	return nil
}

// stringInSlice reports whether target is present in list.
func stringInSlice(target string, list []string) bool {
	for _, item := range list {
		if item == target {
			return true
		}
	}
	return false
}
