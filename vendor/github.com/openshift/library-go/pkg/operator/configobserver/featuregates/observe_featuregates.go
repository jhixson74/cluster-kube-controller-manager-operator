package featuregates

import (
	"fmt"
	"reflect"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/sets"

	configv1 "github.com/openshift/api/config/v1"
	configlistersv1 "github.com/openshift/client-go/config/listers/config/v1"
	"github.com/openshift/library-go/pkg/operator/configobserver"
	"github.com/openshift/library-go/pkg/operator/events"
)

type FeatureGateLister interface {
	FeatureGateLister() configlistersv1.FeatureGateLister
}

func NewObserveFeatureFlagsFunc(knownFeatures sets.String, configPath []string) configobserver.ObserveConfigFunc {
	return (&featureFlags{
		allowAll:      len(knownFeatures) == 0,
		knownFeatures: knownFeatures,
		configPath:    configPath,
	}).ObserveFeatureFlags
}

type featureFlags struct {
	allowAll      bool
	knownFeatures sets.String
	configPath    []string
}

// ObserveFeatureFlags fills in --feature-flags for the kube-apiserver
func (f *featureFlags) ObserveFeatureFlags(genericListers configobserver.Listers, recorder events.Recorder, existingConfig map[string]interface{}) (map[string]interface{}, []error) {
	listers := genericListers.(FeatureGateLister)
	errs := []error{}
	prevObservedConfig := map[string]interface{}{}

	currentConfigValue, _, err := unstructured.NestedStringSlice(existingConfig, f.configPath...)
	if err != nil {
		errs = append(errs, err)
	}
	if len(currentConfigValue) > 0 {
		if err := unstructured.SetNestedStringSlice(prevObservedConfig, currentConfigValue, f.configPath...); err != nil {
			errs = append(errs, err)
		}
	}

	observedConfig := map[string]interface{}{}
	configResource, err := listers.FeatureGateLister().Get("cluster")
	// if we have no featuregate, then the installer and MCO probably still have way to reconcile certain custom resources
	// we will assume that this means the same as default and hope for the best
	if apierrors.IsNotFound(err) {
		configResource = &configv1.FeatureGate{
			Spec: configv1.FeatureGateSpec{
				FeatureGateSelection: configv1.FeatureGateSelection{
					FeatureSet: configv1.Default,
				},
			},
		}
	} else if err != nil {
		errs = append(errs, err)
		return prevObservedConfig, errs
	}

	newConfigValue, err := f.getFeatureNames(configResource)
	if err != nil {
		errs = append(errs, err)
		return prevObservedConfig, errs
	}
	if !reflect.DeepEqual(currentConfigValue, newConfigValue) {
		recorder.Eventf("ObserveFeatureFlagsUpdated", "Updated %v to %s", strings.Join(f.configPath, "."), strings.Join(newConfigValue, ","))
	}

	if err := unstructured.SetNestedStringSlice(observedConfig, newConfigValue, f.configPath...); err != nil {
		recorder.Warningf("ObserveFeatureFlags", "Failed setting %v: %v", strings.Join(f.configPath, "."), err)
		errs = append(errs, err)
	}

	return observedConfig, errs
}

func (f *featureFlags) getFeatureNames(fg *configv1.FeatureGate) ([]string, error) {
	newConfigValue := []string{}
	formatEnabledFunc := func(fs string) string {
		return fmt.Sprintf("%s=true", fs)
	}
	formatDisabledFunc := func(fs string) string {
		return fmt.Sprintf("%s=false", fs)
	}

	if fg.Spec.FeatureSet == configv1.CustomNoUpgrade {
		if fg.Spec.FeatureGateSelection.CustomNoUpgrade == nil {
			return nil, fmt.Errorf(".spec.featureGateSelection.CustomNoUpgrade is empty but the flag %s was set", configv1.CustomNoUpgrade)
		}
		for _, enable := range fg.Spec.FeatureGateSelection.CustomNoUpgrade.Enabled {
			newConfigValue = append(newConfigValue, formatEnabledFunc(enable))
		}
		for _, disabled := range fg.Spec.FeatureGateSelection.CustomNoUpgrade.Disabled {
			newConfigValue = append(newConfigValue, formatDisabledFunc(disabled))
		}
		return newConfigValue, nil
	}

	featureSet, ok := configv1.FeatureSets[fg.Spec.FeatureSet]
	if !ok {
		return nil, fmt.Errorf(".spec.featureSet %q not found", featureSet)
	}
	for _, enable := range featureSet.Enabled {
		// only add whitelisted feature flags
		if !f.allowAll && !f.knownFeatures.Has(enable) {
			continue
		}
		newConfigValue = append(newConfigValue, formatEnabledFunc(enable))
	}
	for _, disable := range featureSet.Disabled {
		// only add whitelisted feature flags
		if !f.allowAll && !f.knownFeatures.Has(disable) {
			continue
		}
		newConfigValue = append(newConfigValue, formatDisabledFunc(disable))
	}

	return newConfigValue, nil
}
