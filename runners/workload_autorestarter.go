package runners

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"time"

	"github.com/go-logr/logr"
	appsV1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

type restarter struct {
	client        client.Client
	metricsClient MetricsClient
	interval      time.Duration
	log           logr.Logger
	recorder      record.EventRecorder
}

func NewRestarter(c client.Client, log logr.Logger, interval time.Duration, recorder record.EventRecorder) manager.Runnable {

	return &restarter{
		client:   c,
		log:      log,
		interval: interval,
		recorder: recorder,
	}
}

// Start implements manager.Runnable
func (c *restarter) Start(ctx context.Context) error {
	ticker := time.NewTicker(c.interval)

	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			err := c.reconcile(ctx)
			if err != nil {
				c.log.Error(err, "failed to reconcile")
				return err
			}
		}
	}
}

type workloads struct {
	deployToStop  map[string]*appsV1.Deployment
	stsToStop     map[string]*appsV1.StatefulSet
	deployToStart map[string]*appsV1.Deployment
	stsToStart    map[string]*appsV1.StatefulSet
	deployTimeout map[string]*appsV1.Deployment
	stsTimeout    map[string]*appsV1.StatefulSet
}

func (c *restarter) reconcile(ctx context.Context) error {
	allPvc, err := c.getAllPVC(ctx)
	if err != nil {
		return err
	}
	allSc, err := c.getAllSc(ctx)
	if err != nil {
		return err
	}
	allDeploy, err := c.getAllDeploy(ctx)
	if err != nil {
		return err
	}
	allSts, err := c.getAllSts(ctx)
	if err != nil {
		return err
	}

	wls := c.getWorkloads(ctx, allPvc, allSc, allDeploy, allSts)

	//stop deploy/sts
	for _, deploy := range wls.deployToStop {
		c.log.WithValues("namespace", deploy.Namespace, "name", deploy.Name).Info("stop deployment")
		err = c.stopDeploy(ctx, deploy)
		if err != nil {
			return err
		}
	}
	for _, sts := range wls.stsToStop {
		c.log.WithValues("namespace", sts.Namespace, "name", sts.Name).Info("stop statefulset")
		err = c.stopSts(ctx, sts)
		if err != nil {
			return err
		}
	}

	//restart
	for _, deploy := range wls.deployToStart {
		c.log.WithValues("namespace", deploy.Namespace, "name", deploy.Name).Info("start deployment")
		err = c.startDeploy(ctx, deploy, false)
		if err != nil {
			return err
		}
	}
	for _, deploy := range wls.deployTimeout {
		c.log.WithValues("namespace", deploy.Namespace, "name", deploy.Name).Info("start deployment of resizing timeout")
		err = c.startDeploy(ctx, deploy, true)
		if err != nil {
			return err
		}
	}
	for _, sts := range wls.stsToStart {
		c.log.WithValues("namespace", sts.Namespace, "name", sts.Name).Info("start statefulset")
		err = c.startSts(ctx, sts, false)
		if err != nil {
			return err
		}
	}
	for _, sts := range wls.stsTimeout {
		c.log.WithValues("namespace", sts.Namespace, "name", sts.Name).Info("start statefulset of resizing timeout")
		err = c.startSts(ctx, sts, true)
		if err != nil {
			return err
		}
	}

	return nil
}

func (c *restarter) getAllPVC(ctx context.Context) (*v1.PersistentVolumeClaimList, error) {
	pvcList := &v1.PersistentVolumeClaimList{}
	err := c.client.List(ctx, pvcList)
	return pvcList, err
}

func (c *restarter) getWorkloads(ctx context.Context, pvcList *v1.PersistentVolumeClaimList, scList *storagev1.StorageClassList, deployList *appsV1.DeploymentList, stsList *appsV1.StatefulSetList) *workloads {
	wls := &workloads{
		deployToStop:  make(map[string]*appsV1.Deployment),
		stsToStop:     make(map[string]*appsV1.StatefulSet),
		deployToStart: make(map[string]*appsV1.Deployment),
		stsToStart:    make(map[string]*appsV1.StatefulSet),
		deployTimeout: make(map[string]*appsV1.Deployment),
		stsTimeout:    make(map[string]*appsV1.StatefulSet),
	}

	pvcs := make([]v1.PersistentVolumeClaim, 0)
	scNeedRestart := c.getRestartSc(ctx, scList)
	for _, pvc := range pvcList.Items {
		if pvc.Spec.StorageClassName != nil {
			if _, ok := scNeedRestart[*pvc.Spec.StorageClassName]; ok {
				pvcs = append(pvcs, pvc)
			} else if val, ok := pvc.Annotations[AutoRestartEnabledKey]; ok {
				needRestart, _ := strconv.ParseBool(val)
				if needRestart {
					pvcs = append(pvcs, pvc)
				}
			}
		}
	}

	for _, pvc := range pvcs {
		sc := c.getScByName(ctx, scList, *pvc.Spec.StorageClassName)
		if sc == nil {
			continue
		}
		for _, condition := range pvc.Status.Conditions {
			switch condition.Type {
			case v1.PersistentVolumeClaimResizing:
				dep := c.getDeploy(ctx, deployList, &pvc)
				if dep != nil {
					namespacedName := dep.Namespace + "/" + dep.Name
					if timeout := c.ifWorkloadTimeout(ctx, sc, dep.Annotations); timeout {
						wls.deployTimeout[namespacedName] = dep
					} else {
						wls.deployToStop[namespacedName] = dep
					}
				}
				sts := c.getSts(ctx, stsList, &pvc)
				if sts != nil {
					namespacedName := sts.Namespace + "/" + sts.Name
					if timeout := c.ifWorkloadTimeout(ctx, sc, sts.Annotations); timeout {
						wls.stsTimeout[namespacedName] = sts
					} else {
						wls.stsToStop[namespacedName] = sts
					}
				}
				break
			case v1.PersistentVolumeClaimFileSystemResizePending:
				dep := c.getDeploy(ctx, deployList, &pvc)
				if dep != nil {
					namespacedName := dep.Namespace + "/" + dep.Name
					wls.deployToStart[namespacedName] = dep
				}
				sts := c.getSts(ctx, stsList, &pvc)
				if sts != nil {
					namespacedName := sts.Namespace + "/" + sts.Name
					wls.stsToStart[namespacedName] = sts
				}
				break
			default:
				continue
			}
		}
	}

	return wls
}

func (c *restarter) getAllSc(ctx context.Context) (*storagev1.StorageClassList, error) {
	scList := &storagev1.StorageClassList{}
	err := c.client.List(ctx, scList)
	return scList, err
}

func (c *restarter) getRestartSc(ctx context.Context, scList *storagev1.StorageClassList) map[string]string {
	scMap := make(map[string]string)
	for _, sc := range scList.Items {
		if val, ok := sc.Annotations[SupportOnlineResize]; ok {
			supportOnline, err := strconv.ParseBool(val)
			if err == nil && !supportOnline {
				if val, ok = sc.Annotations[AutoRestartEnabledKey]; ok {
					needRestart, err := strconv.ParseBool(val)
					if err == nil && needRestart {
						scMap[sc.Name] = ""
					}
				}
			}
		}
	}
	return scMap
}

func (c *restarter) stopDeploy(ctx context.Context, dep *appsV1.Deployment) error {
	zero := int32(0)
	nsName := dep.Namespace + "/" + dep.Name
	logger := c.log.WithName(nsName)

	deploy := &appsV1.Deployment{}
	err := c.client.Get(ctx, client.ObjectKey{Namespace: dep.Namespace, Name: dep.Name}, deploy)
	if err != nil {
		return err
	}
	if deploy.Spec.Replicas == nil {
		logger.Info("Skip stop deploy because it has no replicas")
		return nil
	}
	replicas := *deploy.Spec.Replicas
	if replicas == 0 {
		logger.Info("Skip stop deploy because it has been stopped")
		return nil
	}

	if !c.ensureAnnotationsBeforeStop(deploy.Annotations) {
		logger.Info("Skip stop deploy because it has unexpected annotations", "annotations", deploy.Annotations)
		return nil
	}

	updateDeploy := deploy.DeepCopy()
	updateDeploy.Annotations[RestartStopTime] = strconv.FormatInt(time.Now().Unix(), 10)
	updateDeploy.Annotations[ExpectReplicaNums] = strconv.Itoa(int(replicas))
	updateDeploy.Annotations[RestartStage] = "resizing"
	updateDeploy.Spec.Replicas = &zero
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		e := c.client.Update(ctx, updateDeploy)
		return e
	})
	return err
}

func (c *restarter) ensureAnnotationsBeforeStop(annotations map[string]string) bool {
	if val, ok := annotations[RestartSkip]; ok {
		skip, _ := strconv.ParseBool(val)
		if skip {
			return false
		}
	}
	if stage, ok := annotations[RestartStage]; ok {
		if stage == "resizing" {
			return false
		}
	}
	return true
}

func (c *restarter) stopSts(ctx context.Context, s *appsV1.StatefulSet) error {
	zero := int32(0)
	nsName := s.Namespace + "/" + s.Name
	logger := c.log.WithName(nsName)

	sts := &appsV1.StatefulSet{}
	err := c.client.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: s.Name}, sts)
	if err != nil {
		return err
	}
	if sts.Spec.Replicas == nil {
		logger.Info("Skip stop sts because it has no replicas")
		return nil
	}
	replicas := *sts.Spec.Replicas
	if replicas == 0 {
		logger.Info("Skip stop sts because it has been stopped")
		return nil
	}

	if !c.ensureAnnotationsBeforeStop(sts.Annotations) {
		logger.Info("Skip stop sts because it has unexpected annotations", "annotations", sts.Annotations)
		return nil
	}

	updateSts := sts.DeepCopy()
	updateSts.Annotations[RestartStopTime] = strconv.FormatInt(time.Now().Unix(), 10)
	updateSts.Annotations[ExpectReplicaNums] = strconv.Itoa(int(replicas))
	updateSts.Annotations[RestartStage] = "resizing"
	updateSts.Spec.Replicas = &zero
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		e := c.client.Update(ctx, updateSts)
		return e
	})
	return err
}

func (c *restarter) startDeploy(ctx context.Context, dep *appsV1.Deployment, timeout bool) error {
	nsName := dep.Namespace + "/" + dep.Name
	logger := c.log.WithName(nsName)

	deploy := &appsV1.Deployment{}
	err := c.client.Get(ctx, client.ObjectKey{Namespace: dep.Namespace, Name: dep.Name}, deploy)
	if err != nil {
		return err
	}
	if deploy.Spec.Replicas != nil && *deploy.Spec.Replicas > 0 {
		logger.Info("Skip start deploy because it has been started")
		return nil
	}

	if !c.ensureAnnotationsBeforeStart(deploy.Annotations) {
		logger.Info("Skip start deploy because it has unexpected annotations", "annotations", deploy.Annotations)
		return nil
	}

	rep, _ := strconv.Atoi(deploy.Annotations[ExpectReplicaNums])
	replicas := int32(rep)
	updateDeploy := deploy.DeepCopy()
	if timeout {
		updateDeploy.Annotations[RestartSkip] = "true"
	}
	delete(updateDeploy.Annotations, RestartStopTime)
	delete(updateDeploy.Annotations, ExpectReplicaNums)
	delete(updateDeploy.Annotations, RestartStage)
	updateDeploy.Spec.Replicas = &replicas
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		e := c.client.Update(ctx, updateDeploy)
		return e
	})
	return err
}

func (c *restarter) ensureAnnotationsBeforeStart(annotations map[string]string) bool {
	if _, ok := annotations[RestartStage]; !ok {
		return false
	}
	if annotations[RestartStage] != "resizing" {
		return false
	}
	if _, ok := annotations[ExpectReplicaNums]; !ok {
		return false
	}
	replicas, err := strconv.Atoi(annotations[ExpectReplicaNums])
	if err != nil {
		return false
	}
	return replicas > 0
}

func (c *restarter) startSts(ctx context.Context, s *appsV1.StatefulSet, timeout bool) error {
	nsName := s.Namespace + "/" + s.Name
	logger := c.log.WithName(nsName)

	sts := &appsV1.StatefulSet{}
	err := c.client.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: s.Name}, sts)
	if err != nil {
		return err
	}
	if sts.Spec.Replicas != nil && *sts.Spec.Replicas > 0 {
		logger.Info("Skip start sts because it has been started")
		return nil
	}

	if !c.ensureAnnotationsBeforeStart(sts.Annotations) {
		logger.Info("Skip start sts because it has unexpected annotations", "annotations", sts.Annotations)
		return nil
	}

	rep, _ := strconv.Atoi(sts.Annotations[ExpectReplicaNums])
	replicas := int32(rep)
	updateSts := sts.DeepCopy()
	if timeout {
		updateSts.Annotations[RestartSkip] = "true"
	}
	delete(updateSts.Annotations, RestartStopTime)
	delete(updateSts.Annotations, ExpectReplicaNums)
	delete(updateSts.Annotations, RestartStage)
	updateSts.Spec.Replicas = &replicas
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		e := c.client.Update(ctx, updateSts)
		return e
	})
	return err
}

func (c *restarter) getAllDeploy(ctx context.Context) (*appsV1.DeploymentList, error) {
	deployList := &appsV1.DeploymentList{}
	err := c.client.List(ctx, deployList)
	return deployList, err
}

// getDeploy get the deployment which is using the pvc
// it has an assumption that the pvc is only used by one deployment, which is true in most cases
func (c *restarter) getDeploy(ctx context.Context, deployList *appsV1.DeploymentList, pvc *v1.PersistentVolumeClaim) *appsV1.Deployment {
	for _, deploy := range deployList.Items {
		if len(deploy.Spec.Template.Spec.Volumes) > 0 {
			for _, vol := range deploy.Spec.Template.Spec.Volumes {
				if vol.PersistentVolumeClaim != nil && vol.PersistentVolumeClaim.ClaimName == pvc.Name {
					return &deploy
				}
			}
		}
	}
	return nil
}

func (c *restarter) getAllSts(ctx context.Context) (*appsV1.StatefulSetList, error) {
	stsList := &appsV1.StatefulSetList{}
	err := c.client.List(ctx, stsList)
	return stsList, err
}

// getSts get the statefulset which is using the pvc
// it has an assumption that the pvc is only used by one statefulset, which is true in most cases
func (c *restarter) getSts(ctx context.Context, stsList *appsV1.StatefulSetList, targetPvc *v1.PersistentVolumeClaim) *appsV1.StatefulSet {
	for _, sts := range stsList.Items {
		if len(sts.Spec.Template.Spec.Volumes) > 0 {
			for _, vol := range sts.Spec.Template.Spec.Volumes {
				if vol.PersistentVolumeClaim != nil && vol.PersistentVolumeClaim.ClaimName == targetPvc.Name {
					return &sts
				}
			}
		}
		for _, pvc := range sts.Spec.VolumeClaimTemplates {
			pattern := fmt.Sprintf("^%s-%s-[0-9]+$", pvc.Name, sts.Name)
			match, _ := regexp.MatchString(pattern, targetPvc.Name)
			if match {
				return &sts
			}
		}
	}
	return nil
}

func (c *restarter) ifWorkloadTimeout(ctx context.Context, sc *storagev1.StorageClass, annotations map[string]string) bool {
	maxTime := 300
	if val, ok := sc.Annotations[ResizingMaxTime]; ok {
		userSetTime, err := strconv.Atoi(val)
		if err == nil {
			maxTime = userSetTime
		}
	}
	if startResizeTimeStr, ok := annotations[RestartStopTime]; ok {
		startResizeTime, err := strconv.Atoi(startResizeTimeStr)
		if err == nil && int(time.Now().Unix())-startResizeTime > maxTime {
			return true
		}
	}
	return false
}

func (c *restarter) getScByName(ctx context.Context, scList *storagev1.StorageClassList, name string) *storagev1.StorageClass {
	for _, sc := range scList.Items {
		if sc.Name == name {
			return &sc
		}
	}
	return nil
}
