package walk

import (
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type Country struct {
	Name    string
	Code    string
	Cities  []string
	Details map[string]string
}

type Address struct {
	Street  string
	City    string
	Country *Country
}

type User struct {
	Name          string
	Age           int
	Address       *Address
	Tags          map[string]string
	Friends       []string
	Metadata      map[string]any
	Preferences   map[int]string
	Notifications []*Notification
}

type Notification struct {
	ID      int
	Message string
}

func pathToString(path []string) string {
	var result strings.Builder
	for i, p := range path {
		if strings.HasPrefix(p, "[") {
			result.WriteString(p)
		} else {
			if i > 0 {
				result.WriteString(".")
			}
			result.WriteString(p)
		}
	}
	return result.String()
}

func TestWalk(t *testing.T) {
	t.Run("nilAllocs", func(t *testing.T) {
		user := &User{}

		err := Walk(user, func(v reflect.Value, field *reflect.StructField, path []string) error {
			if v.Kind() == reflect.Pointer && v.IsNil() && v.CanSet() {
				elemType := v.Type().Elem()
				newVal := reflect.New(elemType)
				v.Set(newVal)
				return nil
			}
			if v.Kind() == reflect.Map && v.IsNil() && v.CanSet() {
				newVal := reflect.MakeMap(v.Type())
				v.Set(newVal)
				return nil
			}
			if v.Kind() == reflect.Slice && v.IsNil() && v.CanSet() {
				newVal := reflect.MakeSlice(v.Type(), 0, 0)
				v.Set(newVal)
				return nil
			}
			return nil
		})

		assert.NoError(t, err)
		assert.NotNil(t, user.Address)
		assert.NotNil(t, user.Tags)
		assert.NotNil(t, user.Friends)
		assert.NotNil(t, user.Metadata)
		assert.NotNil(t, user.Preferences)
	})

	t.Run("defaultValues", func(t *testing.T) {
		user := &User{}

		err := Walk(user, func(v reflect.Value, field *reflect.StructField, path []string) error {
			if v.Kind() == reflect.String && v.CanSet() && v.String() == "" {
				v.SetString("default")
			}
			if v.Kind() == reflect.Int && v.CanSet() && v.Int() == 0 {
				v.SetInt(42)
			}
			return nil
		})

		assert.NoError(t, err)
		assert.Equal(t, "default", user.Name)
		assert.Equal(t, 42, user.Age)
	})

	t.Run("pathTracking", func(t *testing.T) {
		user := &User{
			Name: "Alice",
			Age:  30,
			Address: &Address{
				Street: "123 Main St",
				City:   "Metropolis",
			},
			Tags: map[string]string{
				"role": "admin",
			},
			Friends: []string{"Bob", "Charlie"},
		}

		var paths []string

		err := Walk(user, func(v reflect.Value, field *reflect.StructField, path []string) error {
			paths = append(paths, pathToString(path))
			return nil
		})

		assert.NoError(t, err)
		expectedPaths := []string{
			"",
			"Name",
			"Age",
			"Address",
			"Address.Street",
			"Address.City",
			"Address.Country",
			"Tags",
			"Tags.role",
			"Friends",
			"Friends.0",
			"Friends.1",
			"Metadata",
			"Preferences",
			"Notifications",
		}
		for _, p := range expectedPaths {
			assert.Contains(t, paths, p)
		}
	})

	t.Run("nestedNilAllocs", func(t *testing.T) {
		user := &User{
			Address: &Address{},
		}

		err := Walk(user, func(v reflect.Value, field *reflect.StructField, path []string) error {
			if v.Kind() == reflect.Pointer && v.IsNil() && v.CanSet() {
				elemType := v.Type().Elem()
				newVal := reflect.New(elemType)
				v.Set(newVal)
				return nil
			}
			if v.Kind() == reflect.Map && v.IsNil() && v.CanSet() {
				newVal := reflect.MakeMap(v.Type())
				v.Set(newVal)
				return nil
			}
			if v.Kind() == reflect.Slice && v.IsNil() && v.CanSet() {
				newVal := reflect.MakeSlice(v.Type(), 0, 0)
				v.Set(newVal)
				return nil
			}
			return nil
		})

		assert.NoError(t, err)
		assert.NotNil(t, user.Address)
		assert.NotNil(t, user.Address.Country)
		assert.NotNil(t, user.Address.Country.Details)
		assert.NotNil(t, user.Address.Country.Cities)
	})

	t.Run("nestedDefaults", func(t *testing.T) {
		user := &User{
			Address: &Address{
				Country: &Country{},
			},
		}

		err := Walk(user, func(v reflect.Value, field *reflect.StructField, path []string) error {
			if v.Kind() == reflect.String && v.CanSet() && v.String() == "" {
				v.SetString("unknown")
			}
			if v.Kind() == reflect.Int && v.CanSet() && v.Int() == 0 {
				v.SetInt(-1)
			}
			return nil
		})

		assert.NoError(t, err)
		assert.Equal(t, "unknown", user.Name)
		assert.Equal(t, -1, user.Age)
		assert.Equal(t, "unknown", user.Address.Street)
		assert.Equal(t, "unknown", user.Address.City)
		assert.Equal(t, "unknown", user.Address.Country.Name)
		assert.Equal(t, "unknown", user.Address.Country.Code)
	})

	t.Run("nonStringMapKeys", func(t *testing.T) {
		user := &User{
			Preferences: map[int]string{
				1: "option1",
				2: "option2",
			},
		}

		var paths []string

		err := Walk(user, func(v reflect.Value, field *reflect.StructField, path []string) error {
			paths = append(paths, pathToString(path))
			return nil
		})

		assert.NoError(t, err)
		assert.Contains(t, paths, "Preferences")
		assert.Contains(t, paths, "Preferences.1")
		assert.Contains(t, paths, "Preferences.2")
	})

	t.Run("sliceOfPointers", func(t *testing.T) {
		user := &User{
			Notifications: []*Notification{
				{ID: 1, Message: ""},
				nil,
				{ID: 3, Message: "Hello"},
			},
		}

		err := Walk(user, func(v reflect.Value, field *reflect.StructField, path []string) error {
			if v.Kind() == reflect.Pointer && v.IsNil() && v.CanSet() {
				elemType := v.Type().Elem()
				newVal := reflect.New(elemType)
				v.Set(newVal)
				return nil
			}

			if field != nil && field.Name == "Message" && v.CanSet() && v.String() == "" {
				v.SetString("No message")
			}
			return nil
		})

		assert.NoError(t, err)
		assert.Len(t, user.Notifications, 3)
		assert.Equal(t, "No message", user.Notifications[0].Message)
		assert.NotNil(t, user.Notifications[1])
		assert.Equal(t, "No message", user.Notifications[1].Message)
		assert.Equal(t, "Hello", user.Notifications[2].Message)
	})
}

func TestWalkGuardrails(t *testing.T) {
	t.Run("rejectsNonPointerRoot", func(t *testing.T) {
		err := Walk(User{}, func(reflect.Value, *reflect.StructField, []string) error { return nil })
		require.Error(t, err)
	})

	t.Run("rejectsNilRoot", func(t *testing.T) {
		err := Walk((*User)(nil), func(reflect.Value, *reflect.StructField, []string) error { return nil })
		require.Error(t, err)
	})

	t.Run("propagatesCallbackError", func(t *testing.T) {
		user := &User{Name: "Alice"}
		err := Walk(user, func(v reflect.Value, _ *reflect.StructField, path []string) error {
			if len(path) == 1 && path[0] == "Name" {
				return assert.AnError
			}
			return nil
		})
		require.ErrorIs(t, err, assert.AnError)
	})

	t.Run("walksInterfaceValues", func(t *testing.T) {
		type boxed struct {
			Value any
		}
		target := &boxed{Value: map[string]string{"role": "admin"}}
		var paths []string
		err := Walk(target, func(_ reflect.Value, _ *reflect.StructField, path []string) error {
			paths = append(paths, pathToString(path))
			return nil
		})
		require.NoError(t, err)
		assert.Contains(t, paths, "Value.role")
	})

	t.Run("stopsAtNilPointerField", func(t *testing.T) {
		user := &User{Name: "Alice"}
		var paths []string
		err := Walk(user, func(_ reflect.Value, _ *reflect.StructField, path []string) error {
			paths = append(paths, pathToString(path))
			return nil
		})
		require.NoError(t, err)
		assert.Contains(t, paths, "Address")
		assert.NotContains(t, paths, "Address.Street")
	})
}
